; FlyShadow Bar Windows 安装器扩展钩子（由 mygo.json windows.installerInclude 引入）
; 职责：
; 1. 安装完成时自动在 $INSTDIR 生成 fsbar.cmd 并将 $INSTDIR 追加到当前用户 PATH（HKCU\Environment）
; 2. 卸载时清理 fsbar.cmd 并从当前用户 PATH 移除 $INSTDIR

!macro customInstall
  FileOpen $0 "$INSTDIR\fsbar.cmd" w
  FileWrite $0 "@echo off$\r$\n"
  FileWrite $0 '"%~dp0FlyShadow Bar.exe" %*$\r$\n'
  FileClose $0

  StrCpy $1 "$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 +2
  StrCpy $1 "$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 0 fsbar_path_install_done
  InitPluginsDir
  FileOpen $2 "$PLUGINSDIR\fsbar-add-path.ps1" w
  FileWrite $2 "$$d = $$env:FSBAR_INSTDIR.TrimEnd('\')$\r$\n"
  FileWrite $2 "$$p = [Environment]::GetEnvironmentVariable('Path', 'User')$\r$\n"
  FileWrite $2 "$$items = @()$\r$\n"
  FileWrite $2 "if ($$p) { $$items = @($$p -split ';' | Where-Object { $$_.Trim() -ne '' }) }$\r$\n"
  FileWrite $2 "$$exists = $$false$\r$\n"
  FileWrite $2 "foreach ($$it in $$items) { if ($$it.Trim().TrimEnd('\') -ieq $$d) { $$exists = $$true; break } }$\r$\n"
  FileWrite $2 "if (-not $$exists) { $$items += $$d; [Environment]::SetEnvironmentVariable('Path', ($$items -join ';'), 'User') }$\r$\n"
  FileClose $2
  System::Call 'Kernel32::SetEnvironmentVariable(t "FSBAR_INSTDIR", t "$INSTDIR")'
  nsExec::Exec '"$1" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\fsbar-add-path.ps1"'
  Pop $2
  fsbar_path_install_done:
!macroend

!macro customUnInstall
  Delete "$INSTDIR\fsbar.cmd"

  StrCpy $1 "$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 +2
  StrCpy $1 "$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 0 fsbar_path_uninstall_done
  InitPluginsDir
  FileOpen $2 "$PLUGINSDIR\fsbar-rm-path.ps1" w
  FileWrite $2 "$$d = $$env:FSBAR_INSTDIR.TrimEnd('\')$\r$\n"
  FileWrite $2 "$$p = [Environment]::GetEnvironmentVariable('Path', 'User')$\r$\n"
  FileWrite $2 "if ($$p) {$\r$\n"
  FileWrite $2 "  $$items = @($$p -split ';' | Where-Object { $$_.Trim() -ne '' -and $$_.Trim().TrimEnd('\') -ine $$d })$\r$\n"
  FileWrite $2 "  [Environment]::SetEnvironmentVariable('Path', ($$items -join ';'), 'User')$\r$\n"
  FileWrite $2 "}$\r$\n"
  FileClose $2
  System::Call 'Kernel32::SetEnvironmentVariable(t "FSBAR_INSTDIR", t "$INSTDIR")'
  nsExec::Exec '"$1" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\fsbar-rm-path.ps1"'
  Pop $2
  fsbar_path_uninstall_done:
!macroend
