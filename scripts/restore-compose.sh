#!/bin/sh
set -eu

accept_key_loss=no
if [ "${1:-}" = "--accept-federation-key-loss" ]; then
	accept_key_loss=yes
	shift
fi
if [ "$#" -ne 1 ]; then
	echo "usage: $0 [--accept-federation-key-loss] backup.tar.gz" >&2
	exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
case "$1" in
	/*) archive_input=$1 ;;
	*) archive_input=$PWD/$1 ;;
esac
archive_dir=$(CDPATH= cd -- "$(dirname -- "$archive_input")" && pwd -P)
archive_name=$(basename -- "$archive_input")
archive_path=$archive_dir/$archive_name
if [ ! -f "$archive_path" ] || [ -L "$archive_path" ]; then
	echo "backup must be a regular file, not a symbolic link" >&2
	exit 1
fi
case "$archive_dir" in
	*:*|*,*) echo "backup path cannot contain ':' or ','" >&2; exit 2 ;;
esac
for protected_path in "$archive_dir" "$archive_path"; do
	if mode=$(stat -c '%a' "$protected_path" 2>/dev/null); then
		:
	else
		mode=$(stat -f '%Lp' "$protected_path")
	fi
	case "$mode" in
		''|*[!0-7]*) echo "could not read backup permissions" >&2; exit 1 ;;
	esac
	if [ $((0$mode & 077)) -ne 0 ]; then
		echo "backup directory and archive must not be accessible by group or others" >&2
		exit 1
	fi
done
service=${COOKBOOK_COMPOSE_SERVICE:-cookbook}
compose_engine=${COOKBOOK_COMPOSE_ENGINE:-}
if [ -z "$compose_engine" ]; then
	if command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then
		compose_engine=podman
	else
		compose_engine=docker
	fi
fi
case "$compose_engine" in
	docker|podman) command -v "$compose_engine" >/dev/null 2>&1 || { echo "Compose engine is unavailable: $compose_engine" >&2; exit 1; } ;;
	*) echo "COOKBOOK_COMPOSE_ENGINE must be podman or docker" >&2; exit 2 ;;
esac
compose() { "$compose_engine" compose "$@"; }
image_refs=$(compose images -q "$service")
image_ref_count=$(printf '%s\n' "$image_refs" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')
[ "$image_ref_count" = 1 ] || { echo "could not resolve one local image for service $service" >&2; exit 1; }
image_ref=$(printf '%s\n' "$image_refs" | sed '/^[[:space:]]*$/d')
verifier_image=$("$compose_engine" image inspect --format '{{.Id}}' "$image_ref")
printf '%s\n' "$verifier_image" | grep -Eq '^(sha256:)?[0-9a-f]{64}$' || { echo "service image did not resolve to one immutable local image ID" >&2; exit 1; }
snapshot_uid=
snapshot_gid=
if [ "$compose_engine" = docker ]; then
	snapshot_uid=$(id -u) || { echo "could not determine the restore caller UID" >&2; exit 1; }
	snapshot_gid=$(id -g) || { echo "could not determine the restore caller GID" >&2; exit 1; }
	case "$snapshot_uid" in ''|*[!0-9]*) echo "restore caller UID is invalid" >&2; exit 1 ;; esac
	case "$snapshot_gid" in ''|*[!0-9]*) echo "restore caller GID is invalid" >&2; exit 1 ;; esac
fi
cd "$repo_root"
archive_format=$(python3 "$script_dir/validate_backup.py" --allow-legacy --engine "$compose_engine" --image "$verifier_image" --print-format "$archive_path")
if [ "$archive_format" = legacy ] && [ "$accept_key_loss" != yes ]; then
	echo "legacy backup has no federation keys; refusing restore without --accept-federation-key-loss" >&2
	exit 1
fi
if [ "$archive_format" = legacy ]; then
	echo "WARNING: federation identity continuity cannot be established; a new identity will be created after restore" >&2
fi
was_running=no
if compose ps --status running --services | grep -Fx "$service" >/dev/null 2>&1; then
	was_running=yes
fi
staging_dir=$(mktemp -d "${TMPDIR:-/tmp}/cookbook-restore-snapshot.XXXXXXXX")
chmod 700 "$staging_dir"
case "$staging_dir" in *:*|*,*) rmdir "$staging_dir"; echo "temporary snapshot path cannot contain ':' or ','" >&2; exit 1 ;; esac
restore_succeeded=no
cleanup() {
	status=$?
	trap - EXIT
	if [ "$restore_succeeded" = yes ]; then
		if [ "$was_running" = yes ] && ! compose start "$service"; then
			echo "restore succeeded, but the Cookbook service could not be restarted" >&2
			status=1
		fi
	else
		echo "restore failed; Cookbook was left stopped to protect its data" >&2
	fi
	rm -rf -- "$staging_dir"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

compose stop "$service"
compose run -T --rm --no-deps --user 0:0 \
	--volume "$archive_dir:/backup:ro" \
	--volume "$staging_dir:/snapshot" \
	--entrypoint /bin/sh "$service" \
	-ec '
	archive=$1
	archive_format=$2
	snapshot_uid=$3
	snapshot_gid=$4
	if [ -n "$snapshot_uid" ] || [ -n "$snapshot_gid" ]; then
		if [ -z "$snapshot_uid" ] || [ -z "$snapshot_gid" ]; then
			echo "restore snapshot UID/GID pair is incomplete" >&2
			exit 1
		fi
		case "$snapshot_uid" in *[!0-9]*) echo "restore snapshot UID is invalid" >&2; exit 1 ;; esac
		case "$snapshot_gid" in *[!0-9]*) echo "restore snapshot GID is invalid" >&2; exit 1 ;; esac
	fi

	set_snapshot_copy_owner() {
		case "$1" in
			/snapshot/cookbook.db|/snapshot/cookbook.db-wal|/snapshot/cookbook.db-journal) ;;
			*) echo "unexpected restore snapshot path" >&2; exit 1 ;;
		esac
		[ -f "$1" ] && [ ! -L "$1" ] || { echo "restore snapshot copy is not a regular file" >&2; exit 1; }
		if [ -n "$snapshot_uid" ]; then chown "$snapshot_uid:$snapshot_gid" "$1"; fi
		chmod 600 "$1"
	}
	rm -rf /data/media
	rm -rf /data/secrets
	rm -f /data/cookbook.db /data/cookbook.db-wal /data/cookbook.db-shm /data/cookbook.db-journal
	tar -xzf "/backup/$archive" -C /data
	cd /data
	if [ "$archive_format" = 2 ] || [ "$archive_format" = 3 ]; then
		sha256sum -c backup-manifest.sha256
		rm -f backup-manifest.json backup-manifest.sha256
	else
		echo "legacy backup has no federation identity; service start will create a new key" >&2
	fi
	chown -R cookbook:cookbook /data
	chmod 600 /data/cookbook.db
	for sidecar in /data/cookbook.db-wal /data/cookbook.db-journal; do
		[ ! -e "$sidecar" ] || chmod 600 "$sidecar"
	done
	chmod 700 /data/media
	for image in /data/media/*; do
		[ ! -e "$image" ] || chmod 600 "$image"
	done
	if [ -d /data/secrets ]; then
		chown -R cookbook:cookbook /data/secrets
		chmod 700 /data/secrets
		[ ! -e /data/secrets/federation-ed25519.pk8.pem ] || chmod 600 /data/secrets/federation-ed25519.pk8.pem
		[ ! -e /data/secrets/federation-ed25519.pending.pk8.pem ] || chmod 600 /data/secrets/federation-ed25519.pending.pk8.pem
	fi
	for sidecar in cookbook.db-wal cookbook.db-journal; do
		if [ -e "/data/$sidecar" ]; then
			cp -- "/data/$sidecar" "/snapshot/$sidecar"
			set_snapshot_copy_owner "/snapshot/$sidecar"
		fi
	done
	cp -- /data/cookbook.db /snapshot/cookbook.db
	set_snapshot_copy_owner /snapshot/cookbook.db
	' sh "$archive_name" "$archive_format" "$snapshot_uid" "$snapshot_gid"

python3 "$script_dir/validate_backup.py" --allow-legacy --engine "$compose_engine" --image "$verifier_image" --verify-restored-snapshot "$staging_dir" "$archive_path"
restore_succeeded=yes
echo "Restored $archive_path"
