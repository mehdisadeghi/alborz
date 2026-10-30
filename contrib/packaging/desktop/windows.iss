#ifndef AppVersion
  #define AppVersion "0.1.0"
#endif
#ifndef Architecture
  #define Architecture "amd64"
#endif
#ifndef Binary
  #error Supply /DBinary with the full path to Alborz.exe
#endif

[Setup]
AppId={{D390C5AF-A9F1-4B31-87CC-9522473A46A0}
AppName=Alborz
AppVersion={#AppVersion}
AppPublisher=Alborz
DefaultDirName={localappdata}\Programs\Alborz
DefaultGroupName=Alborz
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
MinVersion=10.0.17763
OutputBaseFilename=alborz-{#AppVersion}-windows-{#Architecture}-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupIconFile=Alborz.ico
UninstallDisplayIcon={app}\Alborz.exe
CloseApplications=yes
RestartApplications=no
#if Architecture == "arm64"
ArchitecturesAllowed=arm64
ArchitecturesInstallIn64BitMode=arm64
#else
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#endif

[Files]
Source: "{#Binary}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Alborz"; Filename: "{app}\Alborz.exe"
Name: "{autodesktop}\Alborz"; Filename: "{app}\Alborz.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

[Run]
Filename: "{app}\Alborz.exe"; Description: "Open Alborz"; Flags: nowait postinstall skipifsilent
