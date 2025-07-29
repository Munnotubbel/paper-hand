#!/bin/sh

# Stoppt das Skript sofort, wenn ein Befehl fehlschlägt
set -e

# Umgebungsvariablen prüfen
if [ -z "$POSTGRES_HOST" ] || [ -z "$POSTGRES_USER" ] || [ -z "$POSTGRES_PASSWORD" ] || [ -z "$POSTGRES_DB" ] || [ -z "$BACKUP_S3_BUCKET" ] || [ -z "$BACKUP_S3_ENDPOINT" ] || [ -z "$BACKUP_S3_ACCESS_KEY" ] || [ -z "$BACKUP_S3_SECRET_KEY" ]; then
  echo "Fehler: Nicht alle notwendigen Umgebungsvariablen sind gesetzt."
  exit 1
fi

# AWS CLI für Strato S3 konfigurieren
aws configure set aws_access_key_id "$BACKUP_S3_ACCESS_KEY"
aws configure set aws_secret_access_key "$BACKUP_S3_SECRET_KEY"
aws configure set default.region "$BACKUP_S3_REGION"

# Dateiname für das Backup
BACKUP_FILE="backup-$(date +%Y-%m-%d-%H-%M-%S).sql.gz"
S3_PATH="s3://${BACKUP_S3_BUCKET}/${BACKUP_FILE}"

echo "Erstelle Datenbank-Backup von '${POSTGRES_DB}'..."

# pg_dump ausführen, direkt mit gzip komprimieren
PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -h "$POSTGRES_HOST" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -w | gzip > "$BACKUP_FILE"

echo "Backup erfolgreich erstellt: ${BACKUP_FILE}"
echo "Lade Backup nach S3 hoch: ${S3_PATH}"

# Upload nach S3
aws s3 cp "$BACKUP_FILE" "$S3_PATH" --endpoint-url="$BACKUP_S3_ENDPOINT"

echo "Upload erfolgreich."

# Lokale Backup-Datei löschen
rm "$BACKUP_FILE"

echo "Bereinige alte Backups im S3-Bucket..."

# Alte Backups löschen (die 4 neuesten behalten)
# `aws s3api list-objects-v2` wird verwendet, da es mehr Details liefert
# jq wird verwendet, um das JSON zu parsen und zu sortieren
BACKUPS_TO_DELETE=$(aws s3api list-objects-v2 --bucket "$BACKUP_S3_BUCKET" --endpoint-url="$BACKUP_S3_ENDPOINT" | jq -r '.Contents | sort_by(.LastModified) | .[:-4] | .[] | .Key')

for KEY in $BACKUPS_TO_DELETE; do
  if [ -n "$KEY" ]; then
    echo "Lösche altes Backup: $KEY"
    aws s3 rm "s3://${BACKUP_S3_BUCKET}/${KEY}" --endpoint-url="$BACKUP_S3_ENDPOINT"
  fi
done

echo "Backup-Prozess abgeschlossen."