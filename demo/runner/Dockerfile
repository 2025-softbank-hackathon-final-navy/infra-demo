FROM python:3.9-slim
RUN pip install flask boto3
WORKDIR /app
COPY runner.py /app/runner.py
CMD ["python", "-u", "runner.py"]