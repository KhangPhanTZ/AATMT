; AATMT Windows installer (NSIS 3, Modern UI 2).
; Built by build.sh:  makensis -DVERSION=1.0.0 -DVERSION4=1.0.0.0 -DARCH=amd64 -DSRC=<dir with aatmt.exe> -DOUT=<setup.exe> aatmt.nsi
; Per-user install (no admin rights): %LOCALAPPDATA%\Programs\AATMT. Data stays in %APPDATA%\aatmt
; and is kept on uninstall.

Unicode true
SetCompressor /SOLID lzma

!ifndef VERSION
  !error "pass -DVERSION=x.y.z"
!endif
!ifndef VERSION4
  !error "pass -DVERSION4=x.y.z.w"
!endif
!ifndef ARCH
  !error "pass -DARCH=amd64|arm64"
!endif
!ifndef SRC
  !error "pass -DSRC=<folder containing aatmt.exe, aatmt.ico, README.md>"
!endif
!ifndef OUT
  !error "pass -DOUT=<output installer path>"
!endif

!define APP      "AATMT"
!define EXE      "aatmt.exe"
!define UNINST   "Software\Microsoft\Windows\CurrentVersion\Uninstall\AATMT"

Name "${APP} ${VERSION}"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
InstallDirRegKey HKCU "Software\${APP}" "InstallDir"
RequestExecutionLevel user
BrandingText "${APP} ${VERSION} (${ARCH})"

VIProductVersion "${VERSION4}"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "FileDescription" "AATMT - AI Automation Task Management installer"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Phan Tran Van Khang"

!include "MUI2.nsh"
!define MUI_ICON   "${SRC}\aatmt.ico"
!define MUI_UNICON "${SRC}\aatmt.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_PARAMETERS "-open"
!define MUI_FINISHPAGE_RUN_TEXT "Start AATMT and open it in the browser"
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\README.md"
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "AATMT (required)" SecMain
  SectionIn RO
  ; stop a running copy so the binary can be replaced on upgrade
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  SetOutPath "$INSTDIR"
  File "${SRC}\${EXE}"
  File "${SRC}\aatmt.ico"
  File "${SRC}\README.md"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APP}"
  CreateShortCut "$SMPROGRAMS\${APP}\${APP}.lnk" "$INSTDIR\${EXE}" "-open" "$INSTDIR\aatmt.ico" 0
  CreateShortCut "$SMPROGRAMS\${APP}\Uninstall ${APP}.lnk" "$INSTDIR\uninstall.exe" "" "$INSTDIR\aatmt.ico" 0

  WriteRegStr HKCU "Software\${APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINST}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST}" "Publisher" "Phan Tran Van Khang"
  WriteRegStr HKCU "${UNINST}" "DisplayIcon" "$INSTDIR\aatmt.ico"
  WriteRegStr HKCU "${UNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST}" "NoRepair" 1
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortCut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}" "-open" "$INSTDIR\aatmt.ico" 0
SectionEnd

Section /o "Start AATMT when I sign in" SecStartup
  CreateShortCut "$SMSTARTUP\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\aatmt.ico" 0 SW_SHOWMINIMIZED
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "The AATMT server and web UI (a single program)."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "Put an AATMT shortcut on the desktop."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStartup} "Run AATMT in the background after you sign in to Windows."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\aatmt.ico"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APP}\${APP}.lnk"
  Delete "$SMPROGRAMS\${APP}\Uninstall ${APP}.lnk"
  RMDir "$SMPROGRAMS\${APP}"
  Delete "$DESKTOP\${APP}.lnk"
  Delete "$SMSTARTUP\${APP}.lnk"
  DeleteRegKey HKCU "${UNINST}"
  DeleteRegKey HKCU "Software\${APP}"
  ; user data (%APPDATA%\aatmt) is intentionally kept
SectionEnd
