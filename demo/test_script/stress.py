import requests
import threading
import time
import sys
import random

NODE_IPS = ["10.2.0.203","10.2.0.204","10.2.0.205"]
PORT = "30407"
GATEWAY_URL_TEMPLATE = "http://{node_ip}:" + PORT

def deploy_function():
    user_code = """
import time
def handler(event):
    return f"Finished processing request: {event.get('id')}"
"""

    payload = {
        "name": "test-func",
        "code": user_code,
        "type": "python",
        "request": "small-memory"
    }
    node_ip = random.choice(NODE_IPS)
    print(f"deploy node to {node_ip}")
    gateway_url = GATEWAY_URL_TEMPLATE.format(node_ip=node_ip)
    try:
        res = requests.post(f"{gateway_url}/run", json=payload, timeout=60)
        if res.status_code == 200:
            print(f"Deploy Success to {node_ip}: {res.json()}")
        else:
            print(f"Deploy Failed to {node_ip}: {res.text}")
            sys.exit(1)
    except Exception as e:
        print(f"Connection Error to {node_ip}: {e}")
        sys.exit(1)

def invoke_function(req_id):
    node_ip = random.choice(NODE_IPS)
    gateway_url = GATEWAY_URL_TEMPLATE.format(node_ip=node_ip)

    try:
        payload = {"id": req_id}
        start = time.time()
        res = requests.post(f"{gateway_url}/invoke/test-func", json=payload, timeout=60)
        end = time.time()
        print(f"{res.text}   [Req {req_id}] Status: {res.status_code} {end - start}s (Node: {node_ip})")
    except Exception as e:
        print(f"   [Req {req_id}] Failed: {e} (Node: {node_ip})")

def run_stress_test(concurrency=30):
    print(f"Stress Test {concurrency} thread")
    
    threads = []
    
    for i in range(concurrency):
        t = threading.Thread(target=invoke_function, args=(i,))
        threads.append(t)
        t.start()

    for t in threads:
        t.join()
    
    print("DONE")

if __name__ == "__main__":
    if len(sys.argv) > 1 :
        if sys.argv[1] == "-d":
            print("deploy function")
            deploy_function()
            time.sleep(5)
    run_stress_test(concurrency=30)
