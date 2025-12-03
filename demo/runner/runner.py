from flask import Flask, request, jsonify
import sys
import os
import importlib.util
import boto3
import logging

# Setup logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = Flask(__name__)

USER_CODE_PATH = "/app/user_code.py"

def download_code_from_s3():
    """Download user code from S3 to local file."""
    s3_bucket = os.getenv("S3_BUCKET_NAME")
    s3_key = os.getenv("S3_KEY")
    aws_region = os.getenv("AWS_REGION", "ap-northeast-1")

    if not s3_bucket or not s3_key:
        logger.info("S3_BUCKET_NAME or S3_KEY not set - skipping S3 download")
        return False

    try:
        logger.info(f"Downloading code from S3: s3://{s3_bucket}/{s3_key}")
        s3_client = boto3.client('s3', region_name=aws_region)
        s3_client.download_file(s3_bucket, s3_key, USER_CODE_PATH)
        logger.info(f"Successfully downloaded code from S3 to {USER_CODE_PATH}")
        return True
    except Exception as e:
        logger.error(f"Failed to download from S3: {e}")
        return False

# Try to download code from S3 on startup
if os.getenv("S3_KEY"):
    if download_code_from_s3():
        logger.info("Using code from S3")
    else:
        logger.warning("S3 download failed - will try ConfigMap fallback")
else:
    logger.info("No S3_KEY env var - using ConfigMap mounted code")

# Verify code file exists
if not os.path.exists(USER_CODE_PATH):
    logger.error(f"Code file not found at {USER_CODE_PATH}")
    sys.exit(1)

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
        logger.error(f"Error executing code: {e}")
        return jsonify({"error": str(e), "status": "execution_failed"}), 500

@app.route('/health', methods=['GET'])
def health():
    return jsonify({"status": "ok"})

if __name__ == '__main__':
    logger.info("Starting runner on port 8080")
    app.run(host='0.0.0.0', port=8080)