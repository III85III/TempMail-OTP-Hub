#ifndef AppVersion
  #define AppVersion "1.0.0"
#endif
[Setup]
AppId={{DD94FE38-FCA5-4D97-BF09-F05A842853A3}
AppName=TempMail OTP Hub
AppVersion={#AppVersion}
AppPublisher=III85III
AppPublisherURL=https://github.com/III85III/TempMail-OTP-Hub
DefaultDirName={localappdata}\Programs\TempMail OTP Hub
DefaultGroupName=TempMail OTP Hub
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=TempMail-OTP-Hub-Setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
LicenseFile=..\LICENSE
UninstallDisplayIcon={app}\tempmail-hub.exe
CloseApplications=yes

[Files]
Source: "..\dist\tempmail-hub.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

[Icons]
Name: "{group}\TempMail OTP Hub"; Filename: "{app}\tempmail-hub.exe"
Name: "{autodesktop}\TempMail OTP Hub"; Filename: "{app}\tempmail-hub.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\tempmail-hub.exe"; Description: "Open TempMail OTP Hub"; Flags: nowait postinstall skipifsilent
