# Muster demo. Re-record with: vhs docs/demo.tape
# Needs two machines already in one cluster, with models warm:
#   laptop:   qwen2.5:7b      linuxbox: qwen2.5:0.5b

Output docs/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1800
Set Height 760
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 35ms
Set WindowBar Colorful

Hide
Type "export PS1='$ ' OLLAMA_HOST=localhost:7878 && clear"
Enter
Show

Type "# Two machines on my network. No config, no IPs typed."
Enter
Sleep 500ms
Type "muster status"
Enter
Sleep 3.5s

Type "# Every model from every machine, in one list:"
Enter
Sleep 500ms
Type "ollama list"
Enter
Sleep 3.5s

Type "# qwen2.5:0.5b only exists on the Linux box. Ask for it from the laptop:"
Enter
Sleep 500ms
Type `ollama run qwen2.5:0.5b "In one sentence, why use every computer you own for AI?"`
Enter
Sleep 9s

Type "# Which machine answered? Muster tells you:"
Enter
Sleep 500ms
Type `for m in qwen2.5:0.5b qwen2.5:7b; do curl -s -D- -o/dev/null localhost:7878/api/generate -d "{\"model\":\"$m\",\"prompt\":\"hi\",\"stream\":false,\"options\":{\"num_predict\":4}}" | grep Node; done`
Enter
Sleep 4s

Type "# Health check, with the exact fix when something is wrong:"
Enter
Sleep 500ms
Type "muster doctor"
Enter
Sleep 5s
