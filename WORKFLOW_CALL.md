get:

curl -X GET 'http://localhost:4242/papers?transfer_n8n=false&limit=1' \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL"

update:

curl -X PUT http://localhost:4242/papers/123 \
     -H "Content-Type: application/json" \
     -H "X-API-KEY: DEIN_API_SCHLÜSSEL" \
     -d '{"transfer_n8n": true}'