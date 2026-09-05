@echo off
rem Daily collection from the workstation. Registered in Task Scheduler as
rem "RNFO daily pull"; see docs/OPERATIONS.md.
rem 1. pull sealed days with checksum verification
rem 2. validate the archive against the schema and the registry
rem 3. health: is every probe still writing? alert if not (RNFO_ALERT_URL in .env)
rem 4. mirror the archive to the dedicated foreign host (ssh alias rnfo-archive)
cd /d "%~dp0.."
if not exist data\logs mkdir data\logs
set LOG=data\logs\pull.log
echo ==== %DATE% %TIME% pull >> %LOG%
bin\rnfo-collect.exe pull >> %LOG% 2>&1
echo ==== %DATE% %TIME% validate >> %LOG%
bin\rnfo-collect.exe validate >> %LOG% 2>&1
echo ==== %DATE% %TIME% health >> %LOG%
bin\rnfo-collect.exe health -alert >> %LOG% 2>&1
echo ==== %DATE% %TIME% mirror >> %LOG%
scp -q -r -o BatchMode=yes data rnfo-archive:/var/lib/rnfo-archive/ >> %LOG% 2>&1
echo ==== %DATE% %TIME% done >> %LOG%
