; orOS Drive setup for Windows (Inno Setup 6.5+).
; Built in CI by the Release workflow from files staged by
; packaging/package.sh (out/windows-setup/):
;   iscc /DAppVer=0.2.0 oros-drive.iss
; Installs for the current user only, so it needs no administrator
; rights. One setup for x64 and ARM64 PCs: it installs the matching
; build. The settings stay where the portable zip keeps them
; (%APPDATA%\orOS Drive), so moving between the two keeps them.

#ifndef AppVer
  #define AppVer "0.0.0"
#endif

[Setup]
AppId={{9E3C5B71-4D2A-4F0E-8B6C-2A7D1E5F9C40}
AppName=orOS Drive
AppVersion={#AppVer}
AppVerName=orOS Drive {#AppVer}
AppPublisher=orOS
AppPublisherURL=https://useoros.online
AppSupportURL=https://github.com/koulaxizis/oros-drive/issues
AppUpdatesURL=https://github.com/koulaxizis/oros-drive/releases
VersionInfoVersion={#AppVer}
DefaultDirName={localappdata}\Programs\orOS Drive
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible or arm64
ArchitecturesInstallIn64BitMode=x64compatible or arm64
MinVersion=10.0
OutputDir=.
OutputBaseFilename=orOS-Drive-Setup-{#AppVer}
SetupIconFile=oros-drive.ico
UninstallDisplayIcon={app}\oros-drive.ico
UninstallDisplayName=orOS Drive
LicenseFile=files\LICENSE.txt
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
CloseApplications=no

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "el"; MessagesFile: "Greek.isl"

[CustomMessages]
en.Autostart=Start orOS Drive when I sign in to Windows
el.Autostart=Εκκίνηση του orOS Drive όταν συνδέομαι στα Windows
en.DeleteData=Also delete the orOS Drive settings and sign-in on this computer?%n%nChoose No to keep them for a later install. Your folders, the files in them and your orOS Vault in Dropbox are not touched either way.
el.DeleteData=Να σβηστούν και οι ρυθμίσεις και η σύνδεση του orOS Drive σε αυτόν τον υπολογιστή;%n%nΕπιλέξτε Όχι για να μείνουν για μελλοντική εγκατάσταση. Οι φάκελοί σας, τα αρχεία τους και το orOS Vault στο Dropbox δεν αλλάζουν σε καμία περίπτωση.

[Tasks]
Name: "autostart"; Description: "{cm:Autostart}"
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "files\amd64\*"; DestDir: "{app}"; Flags: ignoreversion; Check: not IsArm64
Source: "files\arm64\*"; DestDir: "{app}"; Flags: ignoreversion; Check: IsArm64
Source: "files\LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "files\THIRD-PARTY-NOTICES.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "oros-drive.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\orOS Drive"; Filename: "{app}\orOS Drive.exe"; WorkingDir: "{app}"; IconFilename: "{app}\oros-drive.ico"
Name: "{userdesktop}\orOS Drive"; Filename: "{app}\orOS Drive.exe"; WorkingDir: "{app}"; IconFilename: "{app}\oros-drive.ico"; Tasks: desktopicon

[Registry]
; The same value the app's own "Start with Windows" setting writes
; (app/autostart_windows.go), so the settings page shows it and can
; turn it off.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "orOS Drive"; ValueData: """{app}\orOS Drive.exe"" run --background"; Tasks: autostart; Flags: uninsdeletevalue

[Run]
Filename: "{app}\orOS Drive.exe"; Description: "{cm:LaunchProgram,orOS Drive}"; Flags: nowait postinstall skipifsilent

[Code]
// The app has no window to close (it lives in the tray), so it is
// stopped outright. The engine is built for that: nothing is deleted
// or committed from a half-done pass.
procedure StopApp();
var
  Code: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM "orOS Drive.exe"', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM orosdrive.exe', '', SW_HIDE, ewWaitUntilTerminated, Code);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopApp();
  Result := '';
end;

// Settings, state and cache go; the Trash folder (files the app moved
// aside) stays, and with it the settings folder if it is not empty.
procedure DeleteSettings(Dir: String);
var
  F: TFindRec;
begin
  if FindFirst(Dir + '\*', F) then
  try
    repeat
      if (F.Name <> '.') and (F.Name <> '..') and (CompareText(F.Name, 'Trash') <> 0) then
      begin
        if F.Attributes and FILE_ATTRIBUTE_DIRECTORY <> 0 then
          DelTree(Dir + '\' + F.Name, True, True, True)
        else
          DeleteFile(Dir + '\' + F.Name);
      end;
    until not FindNext(F);
  finally
    FindClose(F);
  end;
  RemoveDir(Dir);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Code: Integer;
  Exe, Data: String;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Exe := ExpandConstant('{app}\orosdrive.exe');
  StopApp();
  // A folder showing the orOS disk in Explorer goes back to a normal
  // folder: opened files stay, files never opened are in orOS.
  Exec(Exe, 'mount off', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Data := ExpandConstant('{userappdata}\orOS Drive');
  if (not UninstallSilent()) and DirExists(Data) and
     (MsgBox(CustomMessage('DeleteData'), mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
  begin
    Exec(Exe, 'logout', '', SW_HIDE, ewWaitUntilTerminated, Code);
    DeleteSettings(Data);
  end;
end;
