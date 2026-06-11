#### Approval timeout

Human approvals have a default timeout of 30 seconds. To adjust the timeout, update your local Atryum config file (`atryum.toml`), then restart Atryum. For example:

```toml
[defaults]
request_timeout_seconds = 120
```

#### Invocation denials

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to deny.

3. On the invocations detail panel and select **Deny** under Approval Required.

4. (Optional) Enter in a reason for denial — this reason is returned to the agent so you can steer what it does next.

5. Click **Deny** to confirm the denial.