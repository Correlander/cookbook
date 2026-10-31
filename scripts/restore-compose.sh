#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 backup.tar.gz" >&2
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
	if [ $((0$mode & 022)) -ne 0 ]; then
		echo "backup directory and archive must not be group- or world-writable" >&2
		exit 1
	fi
done
python3 "$script_dir/validate_backup.py" "$archive_path"

service=${COOKBOOK_COMPOSE_SERVICE:-cookbook}
backup_dir=${COOKBOOK_BACKUP_DIR:-$repo_root/backups}
if [ -x "$script_dir/backup-compose.sh" ]; then
	COOKBOOK_COMPOSE_SERVICE=$service "$script_dir/backup-compose.sh" "$backup_dir"
else
	echo "backup helper is missing or not executable" >&2
	exit 1
fi

cd "$repo_root"
was_running=no
if docker compose ps --status running --services | grep -Fx "$service" >/dev/null 2>&1; then
	was_running=yes
fi
docker compose stop "$service"

restore_succeeded=no
cleanup() {
	status=$?
	trap - EXIT
	if [ "$restore_succeeded" = yes ]; then
		if [ "$was_running" = yes ] && ! docker compose start "$service"; then
			echo "restore succeeded, but the Cookbook service could not be restarted" >&2
			status=1
		fi
	else
		echo "restore failed; Cookbook was left stopped to protect its data" >&2
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose run -T --rm --no-deps --user 0:0 \
	--volume "$archive_dir:/backup:ro" \
	--entrypoint /bin/sh "$service" \
	-ec '
	archive=$1
	rm -rf /data/media
	rm -f /data/cookbook.db /data/cookbook.db-wal /data/cookbook.db-shm /data/cookbook.db-journal
	tar -xzf "/backup/$archive" -C /data
	chown -R cookbook:cookbook /data
	chmod 600 /data/cookbook.db
	for sidecar in /data/cookbook.db-wal /data/cookbook.db-journal; do
		[ ! -e "$sidecar" ] || chmod 600 "$sidecar"
	done
	chmod 700 /data/media
	for image in /data/media/*; do
		[ ! -e "$image" ] || chmod 600 "$image"
	done
	' sh "$archive_name"

restore_succeeded=yes
echo "Restored $archive_path"
