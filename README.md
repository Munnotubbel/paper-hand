# Paper Hand Backend

Dieses Projekt ist ein Go-basiertes API-Backend zur automatisierten Suche, zum Download und zur Speicherung von wissenschaftlichen Arbeiten von Portalen wie PubMed und EuropePMC. Es ist darauf ausgelegt, hochrelevante, vorqualifizierte Datensätze für nachgelagerte KI-Analysen zu erstellen.

Das System beinhaltet:
- Dynamische Suchanfragen basierend auf Substanzen und wiederverwendbaren Filtern.
- Automatisierten Download von PDF-Dateien, inkl. Entpacken von `.tar.gz`-Archiven.
- Speicherung der PDFs in einem S3-kompatiblen Objektspeicher.
- Speicherung der Metadaten in einer PostgreSQL-Datenbank.
- Einen robusten, automatisierten wöchentlichen Datenbank-Backup-Prozess.
- Rate-Limiting, um die API-Limits der Provider einzuhalten.

---

## Setup & Konfiguration

1.  **Repository klonen**:
    ```bash
    git clone https://github.com/Munnotubbel/paper-hand.git
    cd paper-hand
    ```

2.  **Konfigurationsdatei erstellen**:
    Erstellen Sie eine `.env`-Datei im Hauptverzeichnis. Diese Datei wird sowohl für die lokale Entwicklung als auch für die Produktion auf dem Server benötigt. Füllen Sie alle notwendigen Variablen aus. Eine Vorlage finden Sie in `new.envs`.

    **Wichtige Variablen sind:**
    - `POSTGRES_...`: Zugangsdaten für die lokale oder Produktionsdatenbank.
    - `STRATO_S3_...`: Zugangsdaten für das S3-Bucket, in dem die PDFs gespeichert werden.
    - `BACKUP_S3_...`: Zugangsdaten für das S3-Bucket, in dem die Datenbank-Backups gespeichert werden.
    - `PUBMED_API_KEY`, `UNPAYWALL_EMAIL` etc.

---

## Lokale Entwicklung

Für die lokale Entwicklung wird `docker-compose.yml` verwendet.

```bash
# Startet das Backend und die lokale PostgreSQL-Datenbank
docker-compose up --build
```
Das Backend ist dann unter `http://localhost:4242` erreichbar.

---

## Deployment (Produktion)

Das Deployment erfolgt über die `docker-compose.prod.yml`-Datei.

1.  **Voraussetzungen auf dem Server**:
    - `git`, `docker` und `docker-compose` sind installiert.
    - Das Repository ist geklont.
    - Die `.env`-Datei ist mit den Produktions-Secrets im Projektverzeichnis vorhanden.
    - Sie sind bei der GitHub Container Registry angemeldet (`docker login ghcr.io`).

2.  **Starten & Aktualisieren**:
    ```bash
    # Das neueste Image von GHCR holen
    docker-compose -f docker-compose.prod.yml pull

    # Die Services starten (oder mit den neuen Images neu erstellen)
    docker-compose -f docker-compose.prod.yml up -d --force-recreate
    ```

---

## Desaster Recovery Plan

Dieser Plan beschreibt die Schritte zur Wiederherstellung des Systems auf einem neuen Server nach einem Totalausfall.

### Voraussetzungen

1.  **Neuer Server**: Ein Server mit `docker` und `docker-compose`.
2.  **Repository**: Das Projekt-Repository ist auf den Server geklont.
3.  **`.env`-Datei**: Die `.env`-Datei mit allen Secrets (Datenbank-Passwörter, S3-Keys etc.) wurde im Projektverzeichnis auf dem neuen Server erstellt.
4.  **Datenbank-Backup**: Das letzte erfolgreiche Datenbank-Backup (z.B. `backup-....sql.gz`) wurde aus dem S3-Backup-Bucket heruntergeladen und in das Projektverzeichnis auf dem neuen Server kopiert.

### Schritte zur Wiederherstellung

Führen Sie die folgenden Befehle im Projektverzeichnis auf dem neuen Server aus:

1.  **Nur den Datenbank-Container starten**:
    Dieser Befehl startet nur den PostgreSQL-Service und erstellt das leere Volume für die Daten.
    ```bash
    docker-compose -f docker-compose.prod.yml up -d postgres_meta
    ```
    *Warten Sie einige Sekunden, damit der Container Zeit hat, die Datenbank zu initialisieren.*

2.  **Backup in die Datenbank einspielen**:
    Dieser Befehl entpackt das Backup und leitet es direkt in den `psql`-Client innerhalb des `postgres_meta`-Containers.

    **WICHTIG**: Ersetzen Sie `DEIN_BACKUP_DATEINAME.sql.gz` durch den tatsächlichen Namen Ihrer Backup-Datei.
    ```bash
    gunzip -c DEIN_BACKUP_DATEINAME.sql.gz | docker-compose -f docker-compose.prod.yml exec -T postgres_meta psql -U ${POSTGRES_META_USER} -d ${POSTGRES_META_DB}
    ```

3.  **Alle Services starten**:
    Nachdem die Datenbank wiederhergestellt ist, starten Sie das gesamte System.
    ```bash
    docker-compose -f docker-compose.prod.yml up -d
    ```

**Das System ist nun wiederhergestellt** und läuft mit dem Datenstand des letzten Backups.