# ModLogs

A bot used to post twitch mod actions into a discord channel.

## Using the global version

1. Go to the website https://modlogs.komodohype.dev and add the bot to your discord.
2. Ask your streamer to go to the website https://modlogs.komodohype.dev/login and copy the command that it returns and patse it your discord channel.
3. You can have a maximum of 10 hooks per discord. If you need more you can dm me on discord Troy#0003

If you find any questions, feature requests, bugs, issues or an error is thrown, please make an issue [here](https://github.com/TroyDota/modlogs/issues).

## Commands

### Admin Commands 
- ```/add token minimal? channel? -> Adds a new hook binding for mod logs to the current channel or the channel specified.```

- ```/delete streamerID/streamerName channel? -> Removed the hook for that channel.```

- ```/list channel? -> Lists the current hooks for either the guild or the specified channel.```

- ```/ignore accountName/accountID -> Ignores a user useful for bot.```

- ```/unignore accountName/accountID -> Unignores a user.```

- ```/ignored -> Shows all ignored users```

### Other Commands
- ```/link -> Displays invite links.```

### Screenshots
![image](https://user-images.githubusercontent.com/49777269/111158373-a128cf80-85a0-11eb-9d13-ca634ef56d78.png)
![image](https://user-images.githubusercontent.com/49777269/111158729-10062880-85a1-11eb-8f78-04ff35cd2434.png)


## Running it yourself.

1. Install golang
```bash
wget https://golang.org/dl/go1.16.2.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.16.2.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

2. Clone the repo
```bash
cd /opt/
git clone https://github.com/troydota/modlogs.git
cd modlogs
```

3. Build the source
```bash
go build
```

4. Modify the config.
```bash
cp config.demo.yaml config.yaml && vim config.yaml
```

5. Run the bot with rebuild commands flag once. Once you see the application started message you can stop it and run it in the system service.
```bash
./modlogs --rebuild_commands
# for more info on all the flags
./moglogs --help
```

6. Setup auto run with systemboot.
```bash
sudo cp modlogs.service /etc/systemd/system && sudo systemctl start modlogs && sudo systemctl enable modlogs
```
