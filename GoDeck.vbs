' GoDeck Windows 静默启动器 (无需开启 CMD 控制台窗口)
Set WshShell = CreateObject("WScript.Shell")
strDir = CreateObject("Scripting.FileSystemObject").GetParentFolderName(WScript.ScriptFullName)
strExeDir = strDir & "\dist\windows-amd64"
WshShell.CurrentDirectory = strExeDir

args = ""
For Each arg In WScript.Arguments
    args = args & " """ & arg & """"
Next

WshShell.Run """" & strExeDir & "\GoDeck.exe""" & args, 0, False
