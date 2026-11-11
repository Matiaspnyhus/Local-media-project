; The Windows installer for Local Media (Inno Setup 6).
;
; It installs for the signed-in user only, so it needs no administrator
; rights, with one exception: adding the Windows Firewall rule that lets
; other devices at home reach the server. Windows asks once for that.
;
; It never touches the data folder (%LOCALAPPDATA%\localmedia), where the
; library, sign-in and watch history live. Uninstalling leaves it in place.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\package\LocalMedia"
#endif

[Setup]
AppId={{6F2B8C1E-5A0D-4E57-9B6C-3C1D2A7E4F10}
AppName=Local Media
AppVersion={#AppVersion}
AppPublisher=Local Media project
AppPublisherURL=https://github.com/Matiaspnyhus/Local-media-project
DefaultDirName={localappdata}\Programs\LocalMedia
DisableDirPage=yes
DisableProgramGroupPage=yes
DefaultGroupName=Local Media
PrivilegesRequired=lowest
OutputDir=..\installer
OutputBaseFilename=LocalMedia-Setup
SetupIconFile=..\cmd\localmedia\icon.ico
UninstallDisplayIcon={app}\localmedia.exe
UninstallDisplayName=Local Media
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=no
LicenseFile={#SourceDir}\LICENSE.txt

[Tasks]
Name: "autostart"; Description: "Start Local Media when I sign in to Windows"
Name: "firewall"; Description: "Let other devices at home connect (Windows will ask for permission once)"

[Files]
Source: "{#SourceDir}\localmedia.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\ffprobe.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\THIRD-PARTY.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Local Media"; Filename: "{app}\localmedia.exe"

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "LocalMedia"; ValueData: """{app}\localmedia.exe"" --no-browser"; Flags: uninsdeletevalue; Tasks: autostart

[Run]
Filename: "{app}\localmedia.exe"; Description: "Start Local Media now"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; What automatic updates leave beside the program: earlier versions kept
; for going back. The data folder is somewhere else and is not listed here.
Type: files; Name: "{app}\*.previous"
Type: files; Name: "{app}\*.failed"
Type: files; Name: "{app}\*.stale-*"
Type: files; Name: "{app}\download-*.zip"
Type: dirifempty; Name: "{app}"

[Code]
const
  RuleName = 'Local Media';

procedure StopRunningCopy();
var
  ResultCode: Integer;
begin
  { Only this user's own copy can be stopped without administrator rights,
    which is the only copy this installer manages. }
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM localmedia.exe /F', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Sleep(700);
end;

procedure Elevated(Params: String);
var
  ResultCode: Integer;
begin
  { If the owner says no to the Windows prompt the install still succeeds;
    the app then explains how to allow it later. }
  ShellExec('runas', ExpandConstant('{sys}\cmd.exe'), '/C ' + Params, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopRunningCopy();
  Result := '';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Exe: String;
begin
  if (CurStep = ssPostInstall) and WizardIsTaskSelected('firewall') then
  begin
    Exe := ExpandConstant('{app}\localmedia.exe');
    Elevated('netsh advfirewall firewall delete rule name="' + RuleName + '" & ' +
      'netsh advfirewall firewall add rule name="' + RuleName + '" dir=in action=allow program="' + Exe + '" profile=private,domain enable=yes');
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
  begin
    StopRunningCopy();
    Elevated('netsh advfirewall firewall delete rule name="' + RuleName + '"');
  end;
end;
