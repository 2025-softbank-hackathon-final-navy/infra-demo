from flask import Flask, request, jsonify
import sys
import os
import psutil
from time import time
import importlib.util
from io import StringIO
from contextlib import redirect_stdout

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
        
        stdout_capture = StringIO()
        process = psutil.Process(os.getpid())
        
        start_time = time()
        start_cpu = process.cpu_times()
        start_mem = process.memory_info()

        with redirect_stdout(stdout_capture):
            result = user_module.handler(input_data)

        end_time = time()
        end_cpu = process.cpu_times()
        end_mem = process.memory_info()

        elapsed_time = end_time - start_time
        cpu_user = end_cpu.user - start_cpu.user
        cpu_system = end_cpu.system - start_cpu.system
        mem_diff = end_mem.rss - start_mem.rss 

        logs = stdout_capture.getvalue()
        
        return jsonify({
            "result": result, 
            "status": "success",
            "metrics": {
                "elapsed_time_sec": elapsed_time,
                "cpu_user_time_sec": cpu_user,
                "cpu_system_time_sec": cpu_system,
                "memory_usage_change_bytes": mem_diff
            },
            "logs": logs})
    except Exception as e:
        print(f"Error executing code: {e}", file=sys.stderr)
        return jsonify({"error": str(e), "status": "execution_failed"}), 500

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=8080)