# Alternative Tunneling Services for Local Development

If you don't want to sign up for ngrok, here are free alternatives:

## 1. **LocalTunnel** (No signup required)
```bash
# Install
npm install -g localtunnel

# Use
lt --port 8080
# Gives you: https://random-name.loca.lt
```

## 2. **Serveo** (No signup required)
```bash
# Use SSH tunnel
ssh -R 80:localhost:8080 serveo.net
# Gives you: https://random-name.serveo.net
```

## 3. **Bore** (No signup required)
```bash
# Install
cargo install bore-cli

# Use  
bore local 8080 --to bore.pub
# Gives you: https://random-name.bore.pub
```

## 4. **Cloudflare Tunnel** (Free, requires signup)
```bash
# Install
wget -q https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared-linux-amd64.deb

# Use (no signup needed for quick tunnels)
cloudflared tunnel --url http://localhost:8080
```

## 5. **Zrok** (Free, requires signup)
```bash
# Download and setup
wget https://github.com/openziti/zrok/releases/download/v0.4.23/zrok_0.4.23_linux_amd64.tar.gz
tar -xzf zrok_0.4.23_linux_amd64.tar.gz
sudo mv zrok /usr/local/bin/

# Use
zrok share public http://localhost:8080
```

All of these will give you a public HTTPS URL that you can use in your Slack app configuration.
