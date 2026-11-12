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
Source: "{#SourceDir}\ffmpeg.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\THIRD-PARTY.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\THIRD-PARTY-LICENSES.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Local Media"; Filename: "{app}\localmedia.exe"

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "LocalMedia"; ValueData: """{app}\localmedia.exe"" --no-browser"; Flags: uninsdeletevalue; Tasks: autostart

[Run]
Filename: "{app}\localmedia.exe"; Description: "Start Local Media now"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; What automatic updates leave beside the program: earlier versions kept
; for going back, and a newer version that was unpacked and was still
; waiting for a quiet moment to be installed (a folder "update-<version>"
; and a note beside it). The data folder is somewhere else and is not
; listed here.
Type: files; Name: "{app}\*.previous"
Type: files; Name: "{app}\*.failed"
Type: files; Name: "{app}\*.stale-*"
Type: files; Name: "{app}\download-*.zip"
Type: filesandordirs; Name: "{app}\update-*"
Type: dirifempty; Name: "{app}"

[Code]
const
  RuleName = 'Local Media';
  { How long to wait for the program's files to be let go of: this many
    looks, a fifth of a second apart (20 seconds). }
  FreeLooks = 100;

{ Windows does not let a program's file be replaced or deleted while the
  program runs, and tells so when the file is opened for writing. Nothing
  is written. A file that is not there is not in use. }
function InUse(FileName: String): Boolean;
begin
  Result := False;
  if FileExists(FileName) then
    Result := not SaveStringToFile(FileName, '', True);
end;

{ The running copy starts helper programs (ffmpeg.exe to convert,
  ffprobe.exe to inspect files) and takes them with it when it ends. One
  can be left all the same: started by an earlier version, which did not,
  or in the last moment before the copy was stopped. A helper reading a
  long film through can run for minutes, and for as long as it runs its
  file cannot be replaced. So any that is running from this folder is
  stopped here, whoever started it. Only from this folder: an ffmpeg the
  owner runs for something else is somewhere else, and is left alone.

  Windows itself is asked which programs are running and from where (the
  same service Task Manager's details come from). If it cannot be asked,
  that is noted in the log and the wait below has to do. }
procedure StopHelpersIn(Dir: String);
var
  Locator, Service, Found, Helper: Variant;
  I: Integer;
  Path: String;
begin
  try
    Locator := CreateOleObject('WbemScripting.SWbemLocator');
    Service := Locator.ConnectServer('', 'root\CIMV2', '', '');
    Found := Service.ExecQuery('SELECT ProcessId, ExecutablePath FROM Win32_Process WHERE Name = "ffmpeg.exe" OR Name = "ffprobe.exe"');
    for I := 0 to Found.Count - 1 do
    begin
      Helper := Found.ItemIndex(I);
      { One that ended in this very moment is no longer there to read.
        Another user's program shows no path, and is not ours to stop. }
      if not VarIsNull(Helper) then
        if not VarIsNull(Helper.ExecutablePath) then
        begin
          Path := Helper.ExecutablePath;
          if CompareText(ExtractFilePath(Path), AddBackslash(Dir)) = 0 then
          begin
            Log('Stopping a helper program that was still running: ' + Path);
            Helper.Terminate();
          end;
        end;
    end;
  except
    Log('Could not look for helper programs that are still running: ' + GetExceptionMessage);
  end;
end;

{ Waits until neither the program nor a helper is running from Dir any
  more. False if one still is after the whole wait. }
function FilesAreFree(Dir: String): Boolean;
var
  Look: Integer;
begin
  Result := False;
  for Look := 1 to FreeLooks do
  begin
    if not (InUse(Dir + '\localmedia.exe') or InUse(Dir + '\ffmpeg.exe') or InUse(Dir + '\ffprobe.exe')) then
    begin
      Result := True;
      Exit;
    end;
    Sleep(200);
  end;
end;

{ Stops the running copy and its helpers, and waits until their files can
  be replaced. False if they still cannot be. }
function StopRunningCopy(): Boolean;
var
  ResultCode: Integer;
  Dir: String;
begin
  Dir := ExpandConstant('{app}');
  { Only this user's own copy can be stopped without administrator rights,
    which is the only copy this installer manages. (Not with /T, which
    would also close a film the owner has open in another player: that
    was started by Local Media too.) }
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM localmedia.exe /F', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  StopHelpersIn(Dir);
  Result := FilesAreFree(Dir);
  if not Result then
    Log('The program or one of its helper programs is still running from ' + Dir);
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
  Result := '';
  { Better to stop here and say why than to fail half-way through the
    files with Windows' own "access is denied". }
  if not StopRunningCopy() then
    Result := 'Local Media, or a helper program it started, is still running and could not be stopped, so its files cannot be replaced yet. Wait a minute and run this installer again. Restarting the PC also clears it.';
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
    { If something still holds a file after the wait, the uninstaller
      goes on and removes what it can, as it always has. }
    StopRunningCopy();
    Elevated('netsh advfirewall firewall delete rule name="' + RuleName + '"');
  end;
end;
