curl -X POST http://10.2.0.204:30200/run \
-H "Content-Type: application/json" \
-d '{
    "name": "test5", 
    "code": "def handler(event):\n    print(\"TEST MOCK TEST\")\n    return \"Hell, \" + str(event.get(\"who\", \"World\"))",
    "type": "python",
    "request": "small-memory"
}'