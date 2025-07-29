### Raw Papers Workflow (Datenbeschaffung)

**1. Nächstes unverarbeitetes Paper holen:**
```bash
curl -X GET 'http://localhost:4242/papers?transfer_n8n=false&limit=1' \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL"
```

**2. Paper als an n8n übergeben markieren:**
```bash
curl -X PUT http://localhost:4242/papers/123 \
     -H "Content-Type: application/json" \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL" \
     -d '{"transfer_n8n": true}'
```

---

### Rated Papers Workflow (AI-Analyse-Ergebnisse)

**1. AI-Analyse erstellen oder aktualisieren (Upsert):**
*Hinweis: Dieser eine Befehl funktioniert für beides. Wenn die DOI neu ist, wird ein Eintrag erstellt. Wenn die DOI bereits existiert, werden die Felder aktualisiert.*
```bash
curl -X POST http://localhost:4242/rated-papers \
     -H "Content-Type: application/json" \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL" \
     -d '{
        "doi": "10.1186/s12906-021-03463-5",
        "s3_link": "s3://paper-store/34873737.pdf",
        "rating": 9.1,
        "confidence_score": 0.95,
        "category": "RCT (Human)",
        "ai_summary": "Eine randomisierte, doppelblinde Studie an 120 Teilnehmern zeigte, dass die Einnahme von 1g Curcumin pro Tag über 12 Wochen die kognitive Funktion bei älteren Erwachsenen signifikant verbesserte.",
        "key_findings": "[\"Verbesserung der Gedächtnisleistung\", \"Reduzierung von Entzündungsmarkern\"]",
        "study_strengths": "Doppelblind, Placebo-kontrolliert, klare Endpunkte.",
        "study_limitations": "Homogene Teilnehmergruppe, relativ kurze Beobachtungsdauer.",
        "content_idea": "Video-Titel: \"Dieses Gewürz könnte dein Gehirn im Alter schützen - Neue Studie!\"",
        "content_status": "idee",
        "processed": false
     }'
```

**2. Spezifische AI-Analyse lesen:**
```bash
curl -X GET http://localhost:4242/rated-papers/10.1186%2Fs12906-021-03463-5 \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL"
```
*Hinweis: Die DOI im URL-Pfad muss URL-kodiert sein. Der Schrägstrich `/` wird zu `%2F`.*

**3. Nur einzelne Felder einer AI-Analyse aktualisieren:**
*Du kannst auch nur die Felder senden, die du ändern möchtest. Alle anderen bleiben unberührt.*
```bash
curl -X POST http://localhost:4242/rated-papers \
     -H "Content-Type: application/json" \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL" \
     -d '{
        "doi": "10.1186/s12906-021-03463-5",
        "content_status": "produziert",
        "content_url": "https://www.dein-blog.de/curcumin-studie"
     }'
```