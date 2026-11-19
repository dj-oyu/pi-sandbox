---
name: aws-emulator
description: Interact with the local AWS Emulator (S3, SQS, DynamoDB, etc.) via AWS_ENDPOINT_URL inside pi-sandbox.
---

# Local AWS Emulator Guide

When started with `run-pi-aws.ps1` / `-WithAwsEmulator`, the AWS Emulator is available via the host HTTP->HTTPS bridge at `$AWS_ENDPOINT_URL` with dummy credentials (`AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, `AWS_DEFAULT_REGION=us-east-1`).

## Health Check
```bash
node -e 'fetch(`${process.env.AWS_ENDPOINT_URL}/__admin/health`).then(r=>console.log(r.status))'
```

## SDK Usage
- **Node.js (`@aws-sdk/client-s3`, `@aws-sdk/client-sqs`, `@aws-sdk/client-dynamodb`)**:
  - Pass `endpoint: process.env.AWS_ENDPOINT_URL` (for S3, set `forcePathStyle: true`).
  - Region and credentials are read from `AWS_DEFAULT_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`.
- **Python (`uv add boto3`)**:
  - `boto3.client("s3", endpoint_url=os.environ["AWS_ENDPOINT_URL"])` (or `sqs`, `dynamodb`).

## Lambda Execution (Subcontainer RIE)
- Supports standard AWS Lambda SDK calls (`CreateFunctionCommand` with `Code.ZipFile` base64 zip, `UpdateFunctionCodeCommand`, `GetFunctionCommand`, `DeleteFunctionCommand`, `InvokeCommand`) via `$AWS_ENDPOINT_URL`.
- Alternatively (without zipping), place handler code in `/workspace/functions/<funcName>/index.js` (`exports.handler = async (event, context) => ...`), or map custom paths/handlers in `/workspace/.pi/lambda-routes.json`.
- Each invocation runs in an isolated `pi-lambda-rie` subcontainer (`/var/task:ro`, 256MB) with `$AWS_ENDPOINT_URL` pre-configured so the function can call S3/SQS/DynamoDB on the AWS Emulator.
