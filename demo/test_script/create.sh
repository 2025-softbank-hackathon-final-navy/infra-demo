curl -X POST http://10.2.0.204/run \
-H "Content-Type: application/json" \
-d '{
    "name": "test2", 
    "code": "def handler(event):\n    return \"Hello, \" + str(event.get(\"who\", \"World\"))",
    "type": "python",
    "request": "small-memory"
}'