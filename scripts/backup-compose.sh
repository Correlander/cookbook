#!/bin/sh
set -eu

if [ "$#" -gt 1 ]; then
	echo "usage: $0 [backup-directory]" >&2
	exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
cd "$repo_root"

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
backup_dir=${1:-${COOKBOOK_BACKUP_DIR:-backups}}
if [ -L "$backup_dir" ]; then
	echo "backup directory cannot be a symbolic link" >&2
	exit 1
fi
if [ -e "$backup_dir" ]; then
	[ -d "$backup_dir" ] || { echo "backup path is not a directory" >&2; exit 1; }
else
	mkdir -m 700 -p -- "$backup_dir"
fi
backup_dir=$(CDPATH= cd -- "$backup_dir" && pwd -P)
case "$backup_dir/" in
	"$repo_root/backups/"*) ;;
	"$repo_root/"*) echo "use the ignored backups directory or store backups outside the repository" >&2; exit 2 ;;
esac
case "$backup_dir" in
	*:*|*,*) echo "backup directory cannot contain ':' or ','" >&2; exit 2 ;;
esac
if mode=$(stat -c '%a' "$backup_dir" 2>/dev/null); then
	:
else
	mode=$(stat -f '%Lp' "$backup_dir")
fi
case "$mode" in
	''|*[!0-7]*) echo "could not read backup directory permissions" >&2; exit 1 ;;
esac
if [ $((0$mode & 077)) -ne 0 ]; then
	echo "backup directory must not be accessible by group or others" >&2
	exit 1
fi

archive="cookbook-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
if [ -e "$backup_dir/$archive" ]; then
	echo "backup already exists: $backup_dir/$archive" >&2
	exit 1
fi

was_running=no
if compose ps --status running --services | grep -Fx "$service" >/dev/null 2>&1; then
	was_running=yes
fi
temp_path=$(mktemp "$backup_dir/.cookbook-backup.XXXXXXXX")
temp_name=${temp_path##*/}

cleanup() {
	status=$?
	trap - EXIT
	if [ -n "$temp_path" ]; then
		rm -f "$temp_path"
	fi
	if [ "$was_running" = yes ] && ! compose start "$service"; then
		echo "backup finished, but the Cookbook service could not be restarted" >&2
		status=1
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

compose stop "$service"
compose run -T --rm --no-deps --user 0:0 \
	--volume "$backup_dir:/backup" \
	--entrypoint /bin/sh "$service" \
	-ec '
	temp_name=$1
	uid=$2
	gid=$3
	cd /data
	[ -s cookbook.db ] && [ ! -L cookbook.db ] || { echo "database file is missing or unsafe" >&2; exit 1; }
	[ -d media ] && [ ! -L media ] || { echo "media directory is missing or unsafe" >&2; exit 1; }
	[ -d secrets ] && [ ! -L secrets ] || { echo "federation secrets directory is missing or unsafe" >&2; exit 1; }
	[ "$(stat -c "%a" secrets)" = 700 ] || { echo "federation secrets directory must have mode 700" >&2; exit 1; }
	[ -s secrets/federation-ed25519.pk8.pem ] && [ -f secrets/federation-ed25519.pk8.pem ] && [ ! -L secrets/federation-ed25519.pk8.pem ] || { echo "active federation signing key is missing or unsafe" >&2; exit 1; }
	for key in secrets/*; do
		[ -e "$key" ] || continue
		name=${key#secrets/}
		case "$name" in federation-ed25519.pk8.pem|federation-ed25519.pending.pk8.pem) ;; *) echo "unexpected federation secret file: $name" >&2; exit 1 ;; esac
		[ -f "$key" ] && [ ! -L "$key" ] || { echo "federation keys must be regular files" >&2; exit 1; }
		[ "$(stat -c "%a" "$key")" = 600 ] || { echo "federation key files must have mode 600" >&2; exit 1; }
		[ "$(wc -c < "$key")" -le 4096 ] || { echo "federation key file exceeds the size limit" >&2; exit 1; }
	done
	if find media -type l -print -quit | grep -q .; then
		echo "media directory contains a symbolic link" >&2
		exit 1
	fi
	for file in media/*; do
		[ -e "$file" ] || continue
		name=${file#media/}
		printf "%s\n" "$name" | grep -Eq "^[a-fA-F0-9]{32}\\.(jpg|png|webp)$" || { echo "unexpected media file: $name" >&2; exit 1; }
		[ -f "$file" ] && [ ! -L "$file" ] || { echo "media entries must be regular files" >&2; exit 1; }
	done
	set -- cookbook.db
	for sidecar in cookbook.db-wal cookbook.db-journal; do
		if [ -e "$sidecar" ]; then
			[ -f "$sidecar" ] && [ ! -L "$sidecar" ] || { echo "unsafe SQLite sidecar: $sidecar" >&2; exit 1; }
			set -- "$@" "$sidecar"
		fi
	done
	set -- "$@" media secrets/federation-ed25519.pk8.pem
	if [ -e secrets/federation-ed25519.pending.pk8.pem ]; then set -- "$@" secrets/federation-ed25519.pending.pk8.pem; fi
	umask 077
	tar -czf "/backup/$temp_name" "$@"
	chown "$uid:$gid" "/backup/$temp_name"
	chmod 600 "/backup/$temp_name"
	' sh "$temp_name" "$(id -u)" "$(id -g)"

python3 "$script_dir/validate_backup.py" --finalize "$temp_path"

if ! ln "$temp_path" "$backup_dir/$archive"; then
	echo "could not publish backup without overwriting an existing file" >&2
	exit 1
fi
rm "$temp_path"
temp_path=

printf 'Created private backup: %s/%s\n' "$backup_dir" "$archive"
