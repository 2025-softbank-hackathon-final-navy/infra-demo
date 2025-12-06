FROM python:3.11-slim
RUN pip install flask
RUN pip install requests
RUN pip install numpy
RUN pip install pandas
RUN pip install boto3
RUN pip install pydantic
RUN pip install httpx

WORKDIR /app
COPY runner.py /app/runner.py
CMD ["python", "-u", "runner.py"]