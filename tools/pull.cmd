@echo off
rem Daily collection from the workstation. Registered in Task Scheduler as
rem "RNFO daily pull" by tools/register_pull.ps1; see docs/OPERATIONS.md.
rem Pulls sealed days with checksum verification, then validates the archive.
cd /d "%~dp0.."
if not exist data\logs mkdir data\logs
echo ==== %DATE% %TIME% pull >> data\logs\pull.log
bin\rnfo-collect.exe pull >> data\logs\pull.log 2>&1
echo ==== %DATE% %TIME% validate >> data\logs\pull.log
bin\rnfo-collect.exe validate >> data\logs\pull.log 2>&1
