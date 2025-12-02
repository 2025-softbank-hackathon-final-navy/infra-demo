from flask import Flask, request, jsonify
import sys
import os
import importlib.util

app = Flask(__name__)

# 사용자의 코드는 /app/user_code.py 에 마운트된다고 가정
USER_CODE_PATH = "/app/user_code.py"

@app.route('/', methods=['POST', 'GET'])
def handle_request():
    try:
        spec = importlib.util.spec_from_file_location("user_module", USER_CODE_PATH)
        user_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(user_module)
        
        input_data = request.json if request.is_json else {}
        result = user_module.handler(input_data)
        
        return jsonify({"result": result, "status": "success"})
    except Exception as e:
        print(f"Error executing code: {e}", file=sys.stderr)
        return jsonify({"error": str(e), "status": "execution_failed"}), 500

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=8080)