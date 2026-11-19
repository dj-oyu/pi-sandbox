---
name: aws-emulator
description: Interact with the local AWS Emulator (S3, SQS, DynamoDB), Lambda RIE subcontainers, and Step Functions (ASL) mock via AWS_ENDPOINT_URL inside pi-sandbox.
---

# Local AWS Emulator, Lambda RIE & Step Functions Guide

When started with `run-pi-aws.ps1` / `-WithAwsEmulator`, the AWS endpoint is available at `$AWS_ENDPOINT_URL` with dummy credentials (`AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, `AWS_DEFAULT_REGION=us-east-1`).

## Health Check
```bash
node -e 'fetch(`${process.env.AWS_ENDPOINT_URL}/__admin/health`).then(r=>console.log(r.status))'
```

## S3 / SQS / DynamoDB
- **Node.js (`@aws-sdk/client-s3`, `client-sqs`, `client-dynamodb`)**: pass `endpoint: process.env.AWS_ENDPOINT_URL` (for S3, set `forcePathStyle: true`).
- **Python (`uv add boto3`)**: `boto3.client("s3", endpoint_url=os.environ["AWS_ENDPOINT_URL"])`.

## Lambda Execution (Subcontainer RIE)
- Supports standard AWS Lambda SDK calls (`CreateFunctionCommand` with `Code.ZipFile` base64/raw bytes zip, `UpdateFunctionCodeCommand`, `GetFunctionCommand`, `DeleteFunctionCommand`, `InvokeCommand`).
- Alternatively, place handler code in `/workspace/functions/<funcName>/index.js` or map via `/workspace/.pi/lambda-routes.json`.
- Each invocation runs in an isolated `pi-lambda-rie` subcontainer (`/var/task:ro`, 256MB) with `$AWS_ENDPOINT_URL` pre-configured.

## Step Functions (ASL Mock in Proxy)
- Supports `@aws-sdk/client-sfn`: `CreateStateMachineCommand`, `DescribeStateMachineCommand`, `ListStateMachinesCommand`, `DeleteStateMachineCommand`, `StartExecutionCommand`, `DescribeExecutionCommand`, `GetExecutionHistoryCommand`.
- Supported ASL states: `Pass`, `Task` (invokes Lambda functions registered above via `Resource: "arn:aws:lambda:...:function:<name>"` or `Parameters.FunctionName`), `Choice`, `Wait`, `Succeed`, `Fail`, plus `Retry` / `Catch` / `InputPath` / `ResultPath` / `OutputPath`.
