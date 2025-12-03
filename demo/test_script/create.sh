curl -X POST http://10.0.1.202:30674/run \
-H "Content-Type: application/json" \
-d '{
    "name": "test2", 
    "code": "def handler(event):\n    return \"Hello, \" + str(event.get(\"who\", \"World\"))",
    "type": "python",
    "request": "small-memory"
}'