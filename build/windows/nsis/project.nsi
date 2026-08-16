Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows you to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
## 
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the wails_tools.nsh file, but they can be overwritten here.
####
!define INFO_PROJECTNAME    "AgentPack"
## !define INFO_COMPANYNAME    "My Company" # Default "My Company"
## !define INFO_PRODUCTNAME    "My Product Name" # Default "My Product"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "0.1.0"
## !define INFO_COPYRIGHT      "(c) Now, My Company" # Default "Copyright (c) Now, My Company"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

# The 0.2.3 in-app updater starts this installer via CreateProcess with
# HideWindow=true, which passes wShowWindow=SW_HIDE in STARTUPINFO. NSIS honors
# that nCmdShow and creates its main dialog HIDDEN (process alive in background,
# no window, no UAC). Force the dialog visible on GUI init so in-app updates
# always surface the installer UI. See NSIS docs for BringToFront (SW_HIDE case).
!define MUI_CUSTOMFUNCTION_GUIINIT ForceVisibleGuiInit

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uninstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

# Remember the last install directory in the registry so upgrades/reinstalls
# restore it and the user does not have to re-pick a location every time.
!define AGENTPACK_INSTALL_DIR_KEY "Software\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!define AGENTPACK_INSTALL_DIR_VALUE "InstallDir"

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
## They are inactive by default; define WAILS_SIGN_INSTALLER (e.g. `makensis -DWAILS_SIGN_INSTALLER`)
## with signtool on PATH to enable. !finalize cannot be conditional on its own, so it is wrapped in !ifdef.
!ifdef WAILS_SIGN_INSTALLER
!uninstfinalize 'signtool --file "%1"'
!finalize 'signtool --file "%1"'
!endif

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
!else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture

   # Restore the previously saved install directory (overrides InstallDir), so
   # upgrades do not force the user to manually re-pick a location.
   # Check HKLM first (machine-scope installs) then fall back to HKCU
   # (per-user installs) to support both.
   ReadRegStr $0 HKLM "${AGENTPACK_INSTALL_DIR_KEY}" "${AGENTPACK_INSTALL_DIR_VALUE}"
   StrCmp $0 "" try_hkcu
   Goto restore_dir
   try_hkcu:
   ReadRegStr $0 HKCU "${AGENTPACK_INSTALL_DIR_KEY}" "${AGENTPACK_INSTALL_DIR_VALUE}"
   StrCmp $0 "" skip_restore_dir
   restore_dir:
   StrCpy $INSTDIR $0
   skip_restore_dir:
FunctionEnd

# Called by MUI right after the installer dialog is created. Undo a possible
# SW_HIDE inherited from the parent process STARTUPINFO (0.2.3 updater) and
# bring the window to the front so the installer UI is always visible.
Function ForceVisibleGuiInit
    ShowWindow $HWNDPARENT 5 # 5 = SW_SHOW
    BringToFront
FunctionEnd

Section
    !insertmacro wails.setShellContext

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR
    
    !insertmacro wails.files

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols
    
    !insertmacro wails.writeUninstaller
    # Save the install directory for the next upgrade/reinstall
    WriteRegStr HKLM "${AGENTPACK_INSTALL_DIR_KEY}" "${AGENTPACK_INSTALL_DIR_VALUE}" "$INSTDIR"
SectionEnd

Section "uninstall" 
    !insertmacro wails.setShellContext

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller

    # Clean up the remembered install directory on uninstall
    DeleteRegKey HKLM "${AGENTPACK_INSTALL_DIR_KEY}"
SectionEnd