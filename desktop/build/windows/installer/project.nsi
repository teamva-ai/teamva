Unicode true

####
## Teamva NSIS installer.
##
## Wails writes wails_tools.nsh next to this file before running makensis. It
## resolves INFO_* / PRODUCT_EXECUTABLE from wails.json and supplies the
## WebView2, shortcut, registry and uninstaller macros.
##
## The app resolves its bundled OpenCode runtime at
##   <exe dir>\Resources\opencode\<platform>\opencode.exe
## so the installer must copy Resources next to Teamva.exe, not only the exe.
####

!include "wails_tools.nsh"

VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_FINISHPAGE_NOAUTOCLOSE
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"
## The language dialog picks between them; silent installs use English.
!insertmacro MUI_RESERVEFILE_LANGDLL

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe"
InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
ShowInstDetails show

## Repository root relative to desktop\build\windows\installer.
!define TEAMVA_ROOT "..\..\..\.."

Function .onInit
   !insertmacro MUI_LANGDLL_DISPLAY
   !insertmacro wails.checkArchitecture
FunctionEnd

Section
    !insertmacro wails.setShellContext

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR

    !insertmacro wails.files

    ; Bundled runtime and license files. Keep the platform folder name in
    ; sync with embedded.bundledPlatformName: GOARCH amd64 ships as x64.
    SetOutPath "$INSTDIR\Resources\opencode\windows-x64"
    File "${TEAMVA_ROOT}\vendor\opencode\windows-x64\opencode.exe"

    SetOutPath "$INSTDIR"
    File "${TEAMVA_ROOT}\LICENSE"
    File "${TEAMVA_ROOT}\NOTICE"
    File "${TEAMVA_ROOT}\THIRD_PARTY_NOTICES.md"

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    !insertmacro wails.writeUninstaller
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
    Delete "$INSTDIR\Resources\opencode\windows-x64\opencode.exe"
    RMDir "$INSTDIR\Resources\opencode\windows-x64"
    RMDir "$INSTDIR\Resources\opencode"
    RMDir "$INSTDIR\Resources"
    Delete "$INSTDIR\LICENSE"
    Delete "$INSTDIR\NOTICE"
    Delete "$INSTDIR\THIRD_PARTY_NOTICES.md"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
