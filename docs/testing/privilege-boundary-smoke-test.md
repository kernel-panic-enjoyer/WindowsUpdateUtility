# Privilege Boundary Windows Smoke Test

Scope: manual validation for the medium-integrity WebUI coordinator and elevated worker boundary.

Build under test:

```powershell
$root='C:\Users\User\Documents\Updater'
Set-Location $root
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

## 1. Administrator Account

1. Sign in as an administrator.
2. Start `dist\WindowsUpdaterWebUI.exe` normally from Explorer or PowerShell without "Run as administrator".
3. Confirm Task Manager does not show the WebUI process as elevated.
4. Confirm Store inventory and Store actions run without a startup UAC prompt.
5. Start a Chocolatey or WinGet install/update.
6. Confirm UAC appears only when the privileged action starts.
7. Confirm the operation returns a typed result in the WebUI and the app remains responsive.

Expected result: the coordinator stays medium-integrity; only the worker is elevated.

## 2. Standard Account With Separate Admin Credentials

1. Sign in as a standard user.
2. Start the app normally.
3. Confirm no startup UAC prompt appears.
4. Start a Chocolatey or WinGet install/update.
5. Enter separate administrator credentials at UAC.
6. Confirm the worker operation completes or returns an explicit package-manager error.
7. Confirm Store inventory still reflects the standard user's Store/AppX context, not the administrator account.

Expected result: the elevated worker can perform admin work, while Store detection remains current-user scoped.

## 3. Multiple Signed-In Sessions

1. Sign in as user A and start the app.
2. Switch user and sign in as user B.
3. Start the app as user B.
4. Trigger a privileged action in each session.
5. Confirm each WebUI only receives results for its own request and does not show the other user's package state or worker result.

Expected result: per-launch capability, request ID, user SID, and session ID keep the two coordinators isolated.

## 4. UAC Cancellation

1. Start the app as a non-elevated user.
2. Trigger a Chocolatey or WinGet install/update.
3. Cancel the UAC prompt.
4. Confirm the WebUI shows an explicit elevated-worker startup/UAC error.
5. Confirm the request does not hang and later operations can still be started.

Expected result: UAC cancellation returns an error and does not leave the job running forever.

## 5. Worker Crash

1. Start a privileged operation.
2. Terminate the elevated worker process from Task Manager while the operation is running.
3. Confirm the WebUI receives an IPC disconnect or worker-death error.
4. Confirm the app remains running and can start a later operation.

Expected result: worker death is reported as an explicit operation failure.

## 6. Browser Reload During Operation

1. Start a long privileged package operation.
2. Reload the WebUI browser tab.
3. Confirm the current operation remains visible through job status polling.
4. Confirm completion or failure is reported after the worker returns.

Expected result: browser reload does not cancel the backend operation or lose the final result.

## 7. Malformed IPC Probe

1. While the app is running, attempt to connect to the worker pipe name from another local process if discoverable.
2. Send malformed JSON, the wrong protocol version, the wrong capability, and an unknown operation.
3. Confirm the worker rejects the message and does not execute a command.

Expected result: unknown operations, unexpected fields, wrong user/session, wrong capability, and malformed messages are rejected.
