@echo off
rem Hourly health check from the workstation. Registered in Task Scheduler as
rem "RNFO health". This is the only watcher that can see every probe and the
rem responder at once; the watchers on the servers cover what they can reach
rem when this machine is off.
cd /d "%~dp0.."
if not exist data\logs mkdir data\logs
echo ==== %DATE% %TIME% health >> data\logs\health.log
bin\rnfo-collect.exe health -alert -responder >> data\logs\health.log 2>&1
