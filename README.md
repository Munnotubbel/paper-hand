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

---

## API-Dokumentation

Das Backend stellt eine REST-API zur Verfügung, die unter `http://localhost:4242` (lokal) oder `http://paper-backend:4242` (Docker) erreichbar ist.

### Authentifizierung

Alle API-Calls benötigen einen API-Key im Header:
```
X-API-Key: your-api-key-here
```

---

## 📄 Papers API

### GET `/papers`
Ruft alle Papers ohne Filter ab.

**Response:**
```json
[
  {
    "id": 1,
    "created_at": "2024-01-29T20:16:00Z",
    "updated_at": "2024-01-29T20:16:00Z",
    "substance": "curcumin",
    "pmid": "30574426",
    "doi": "10.14336/AD.2018.1026",
    "title": "Effects of curcumin on memory...",
    "abstract": "Background and purpose...",
    "study_date": "2018-10-26T00:00:00Z",
    "authors": "Small GW, Siddarth P, Li Z...",
    "public_url": "https://pubmed.ncbi.nlm.nih.gov/30574426/",
    "download_link": "https://example.com/paper.pdf",
    "download_date": "2024-01-29T19:00:00Z",
    "transfer_n8n": false,
    "cloud_stored": true,
    "study_type": "clinical trial",
    "publication_type": "Journal Article",
    "study_design": "randomized controlled trial",
    "no_pdf_found": false,
    "s3_link": "https://s3.hidrive.strato.com/paper-store/30574426.pdf"
  }
]
```

### POST `/papers/query`
Erweiterte Suche mit Filtern in Papers.

**Request Body:**
```json
{
  "substance": "curcumin",
  "transfer_n8n": false,
  "cloud_stored": true,
  "no_pdf_found": false,
  "limit": 10
}
```

**Verfügbare Filter:**
- `substance` (string): Filtert nach Substanz
- `transfer_n8n` (boolean): Filtert nach Transfer-Status
- `cloud_stored` (boolean): Filtert nach Cloud-Storage Status
- `no_pdf_found` (boolean): Filtert nach PDF-Verfügbarkeit
- `limit` (int): Begrenzt Anzahl der Ergebnisse

### PUT `/papers/:id`
Aktualisiert ein bestehendes Paper.

**Request Body:** Beliebige Paper-Felder zum Updaten
```json
{
  "transfer_n8n": true,
  "cloud_stored": true
}
```

---

## 🧪 Substances API

### GET `/substances`
Ruft alle verfügbaren Substanzen ab.

**Response:**
```json
[
  {
    "id": 1,
    "created_at": "2024-01-29T20:16:00Z",
    "updated_at": "2024-01-29T20:16:00Z",
    "name": "curcumin"
  }
]
```

### POST `/substances`
Erstellt eine neue Substanz.

**Request Body:**
```json
{
  "name": "resveratrol"
}
```

### PUT `/substances/:id`
Aktualisiert eine Substanz.

### DELETE `/substances/:id`
Löscht eine Substanz.

---

## 🔍 Search Filters API

### GET `/search-filters`
Ruft alle Suchfilter ab.

**Response:**
```json
[
  {
    "id": 1,
    "created_at": "2024-01-29T20:16:00Z",
    "updated_at": "2024-01-29T20:16:00Z",
    "name": "Meta-Analysis (Human)",
    "filter_query": "\"meta-analysis\"[Publication Type] OR \"systematic review\"[Publication Type]) AND \"humans\"[MeSH Terms]"
  }
]
```

### POST `/search-filters`
Erstellt einen neuen Suchfilter.

**Request Body:**
```json
{
  "name": "Custom Filter",
  "filter_query": "\"randomized controlled trial\"[Publication Type]"
}
```

### PUT `/search-filters/:id`
Aktualisiert einen Suchfilter.

### DELETE `/search-filters/:id`
Löscht einen Suchfilter.

---

## 🔎 Search API

### POST `/search/:substance_name`
Startet eine asynchrone Suche für eine bestimmte Substanz.

**Response:**
```json
{
  "message": "Search for substance curcumin triggered."
}
```

**Hinweis:** Die Suche läuft asynchron im Hintergrund. Neue Papers erscheinen in der Papers-Datenbank.

---

## ⭐ Rated Papers API

### POST `/rated-papers`
Erstellt oder aktualisiert ein bewertetes Paper (Upsert basierend auf DOI).

**Request Body:**
```json
{
  "doi": "10.14336/AD.2018.1026",
  "s3_link": "https://s3.hidrive.strato.com/paper-store/30574426.pdf",
  "rating": 8,
  "confidence_score": 0.75,
  "category": "Priorität 2: Solide Grundlage",
  "ai_summary": "Eine 18-Monate lange, randomisierte, placebokontrollierte Studie zeigt...",
  "key_findings": "[\"Kernaussage 1\",\"Kernaussage 2\",\"Kernaussage 3\"]",
  "study_strengths": "Double-blind, placebokontrolliertes RCT; lange Studienlaufzeit...",
  "study_limitations": "Kleine Teilnehmerzahl; nur nicht-demente Probanden...",
  "content_idea": "Curcumin-Studie: So stark boostert das goldene Gewürz Ihr Gedächtnis!",
  "content_status": "idee",
  "processed": false
}
```

### GET `/rated-papers/:doi`
Ruft ein bewertetes Paper anhand der DOI ab (automatisch erweitert um PMID und Substance aus rawDB).

**Response:**
```json
{
  "id": 123,
  "doi": "10.14336/AD.2018.1026",
  "rating": 8,
  "confidence_score": 0.75,
  "category": "Priorität 2: Solide Grundlage",
  "ai_summary": "Eine 18-Monate lange, randomisierte...",
  "key_findings": "[\"Kernaussage 1\",\"Kernaussage 2\"]",
  "content_idea": "Curcumin-Studie: So stark boostert...",
  "pmid": "30574426",
  "substance": "curcumin"
}
```

### POST `/rated-papers/query`
Erweiterte Suche in bewerteten Papers.

**Request Body:**
```json
{
  "min_rating": 6.1,
  "category_keywords": [
    "Solide Grundlage",
    "Content-Gold",
    "Interessanter Ansatz"
  ],
  "content_status": "idee",
  "processed": false,
  "limit": 10
}
```

**Verfügbare Filter:**
- `doi` (string): Spezifische DOI
- `min_rating` (float): Rating >= Wert
- `category_keywords` ([]string): OR-Suche in Category-Feld (case-insensitive)
- `content_status` (string): Content Status
- `processed` (boolean): Verarbeitungs-Status
- `limit` (int): Begrenzt Anzahl der Ergebnisse

**Response:** Array von RatedPaper-Objekten, sortiert nach Rating (absteigend) und Erstellungsdatum. Jeder Eintrag wird automatisch um PMID und Substance aus rawDB erweitert.

```json
[
  {
    "id": 123,
    "doi": "10.14336/AD.2018.1026",
    "rating": 8,
    "confidence_score": 0.75,
    "category": "Priorität 2: Solide Grundlage",
    "ai_summary": "Eine 18-Monate lange, randomisierte...",
    "key_findings": "[\"Kernaussage 1\",\"Kernaussage 2\"]",
    "content_idea": "Curcumin-Studie: So stark boostert...",
    "content_status": "idee",
    "processed": false,
    "pmid": "30574426",
    "substance": "curcumin"
  },
  {
    "id": 124,
    "doi": "10.1234/example.2024",
    "rating": 7,
    "category": "Content-Gold",
    "pmid": "12345678",
    "substance": "resveratrol"
  }
]
```

---

## 📝 Content Articles API

### POST `/content-articles`
Erstellt einen neuen Content-Artikel.

**Request Body:**
```json
{
  "id": 123,
  "substance": "curcumin",
  "pmid": "30574426",
  "doi": "10.14336/AD.2018.1026",
  "study_link": "https://pubmed.ncbi.nlm.nih.gov/30574426/",
  "rating": 8.5,
  "title": "Curcumin boostert Ihr Gedächtnis",
  "subtitle": "Neue Studie zeigt erstaunliche Ergebnisse",
  "text": "Vollständiger Artikel-Text...",
  "picture_url": "https://example.com/image.jpg",
  "study_type": "RCT",
  "study_release_date": "2018-10-26T00:00:00Z",
  "content_status": "draft",
  "author_name": "Max Mustermann",
  "blog_posted": false,
  "meta_description": "SEO-optimierte Beschreibung",
  "slug": "curcumin-gedaechtnis-studie-2024",
  "category": "Nahrungsergänzung",
  "tags": "[\"Curcumin\",\"Gedächtnis\",\"Alzheimer\"]"
}
```

### PUT `/content-articles/:id`
Aktualisiert einen bestehenden Content-Artikel.

**Request Body:** Beliebige Felder zum Updaten
```json
{
  "content_status": "published",
  "published_at": "2024-01-29T20:16:00Z",
  "blog_posted": true
}
```

### GET `/content-articles/:id`
Ruft einen Content-Artikel anhand der ID ab.

### POST `/content-articles/query`
Erweiterte Suche in Content-Artikeln.

**Request Body:**
```json
{
  "substance": "curcumin",
  "pmid": "30574426",
  "doi": "10.14336/AD.2018.1026",
  "content_status": "published",
  "category": "Nahrungsergänzung",
  "author_name": "Max Mustermann",
  "study_type": "RCT",
  "blog_posted": false,
  "limit": 10
}
```

**Verfügbare Filter:**
- `substance` (string): Filtert nach Substanz
- `pmid` (string): PubMed ID
- `doi` (string): DOI
- `content_status` (string): Content Status (draft, review, published, archived)
- `category` (string): Kategorie
- `author_name` (string): Autor
- `study_type` (string): Studientyp
- `blog_posted` (boolean): Blog-Veröffentlichungs-Status
- `limit` (int): Begrenzt Anzahl der Ergebnisse

---

## Content Management Workflow

### Typischer Workflow für Content-Erstellung:

1. **Paper Discovery**: Papers werden über `/search/:substance_name` gefunden
2. **AI-Bewertung**: Papers werden bewertet und in `/rated-papers` gespeichert
3. **Content-Selektion**: Hochwertige Papers (Rating > 6) werden über `/rated-papers/query` gefiltert
4. **Content-Erstellung**: Basierend auf RatedPaper wird ein ContentArticle erstellt
5. **Content-Workflow**: `draft` → `review` → `published` → Blog-Posting (`blog_posted: true`)

### Content Status Definitionen:
- **`draft`**: Artikel in Bearbeitung
- **`review`**: Artikel zur Review bereit
- **`published`**: Artikel freigegeben, bereit für Veröffentlichung
- **`archived`**: Artikel archiviert

---

## 📊 Monitoring

### GET `/metrics`
Prometheus-Metriken für Monitoring (siehe Prometheus-Format).

---

## Beispiel-Workflows

### 1. Neue Substanz hinzufügen und durchsuchen:
```bash
# Substanz hinzufügen
curl -X POST http://localhost:4242/substances \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"name": "resveratrol"}'

# Suche starten
curl -X POST http://localhost:4242/search/resveratrol \
  -H "X-API-Key: your-key"
```

### 2. Hochwertige Papers für Content finden:
```bash
curl -X POST http://localhost:4242/rated-papers/query \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "min_rating": 6.1,
    "category_keywords": ["Content-Gold", "Solide Grundlage"],
    "content_status": "idee",
    "limit": 5
  }'
```

### 3. Content-Artikel erstellen und verwalten:
```bash
# Artikel erstellen
curl -X POST http://localhost:4242/content-articles \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{...article data...}'

# Als veröffentlicht markieren
curl -X PUT http://localhost:4242/content-articles/123 \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"content_status": "published", "blog_posted": true}'
```