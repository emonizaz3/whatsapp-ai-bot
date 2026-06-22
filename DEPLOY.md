# Deploying to Render.com

This guide provides step-by-step instructions for deploying your WhatsApp AI Bot and Web Dashboard to [Render](https://render.com). 

Because this application uses a SQLite database (`store.db`), persistent configurations (`config.json`), and activity logs (`logs.json`), you **must** configure a Persistent Disk. Without it, you will have to scan the QR code to pair WhatsApp every time the server restarts or redeploys.

---

## Deployment Steps

### Step 1: Create a New Web Service on Render
1. Log in to your [Render Dashboard](https://dashboard.render.com).
2. Click **New +** in the top right and select **Web Service**.
3. Connect your GitHub repository containing this project's code.
4. Configure the basic settings:
   * **Name:** `whatsapp-ai-bot` (or your preferred name)
   * **Language:** `Go`
   * **Branch:** `main` (or your default branch)
   * **Build Command:** `go build -o app main.go`
   * **Start Command:** `./app`
   * **Plan:** Select your desired plan. 
     *(Note: Free instance web services sleep after 15 minutes of inactivity, which will temporarily pause the bot. To keep the bot awake 24/7, a paid Starter plan is recommended, or you can use an uptime monitor service to periodically ping the `/ping` endpoint).*

---

### Step 2: Attach a Persistent Disk (Critical for SQLite)
Since Render containers have ephemeral filesystems, any files created during runtime (such as database sessions and logs) are destroyed upon redeployment. A persistent disk keeps your data safe.

1. In your Web Service settings page, click on the **Disks** tab in the left-hand menu.
2. Click **Add Disk**.
3. Configure the disk settings:
   * **Name:** `whatsapp-data`
   * **Mount Path:** `/data` (This is where the bot will store configurations, sessions, and logs)
   * **Size:** `1 GB` (This is the minimum size and is more than enough for storing SQLite files and configurations)
4. Click **Create Disk**.

---

### Step 3: Configure Environment Variables
1. Click on the **Environment** tab in the left-hand menu.
2. Click **Add Environment Variable** and define the following variables:

| Environment Variable | Value / Description |
| :--- | :--- |
| `DATA_DIR` | `/data` *(Tells the bot to use the mounted persistent disk folder)* |
| `GROQ_API_KEY` | `your_groq_api_key` *(Your Groq API token)* |
| `DASHBOARD_USERNAME` | `your_chosen_username` *(Secures your web panel with this username)* |
| `DASHBOARD_PASSWORD` | `your_chosen_secure_password` *(Secures your web panel with this password)* |
| `PORT` | `8080` *(Render will bind its routing traffic to this port)* |

3. Click **Save Changes**. Render will automatically trigger a new deployment.

---

### Step 4: Access and Link Your WhatsApp Device
1. Once the build completes and the logs display `Dashboard Web Server Active on port 8080`, click on your Web Service URL (e.g., `https://whatsapp-ai-bot.onrender.com`).
2. Log in using the `DASHBOARD_USERNAME` and `DASHBOARD_PASSWORD` credentials you set in the environment variables.
3. Click **Link Device** in the authentication card:
   * **Option A (QR Code):** Open WhatsApp on your primary phone, navigate to **Settings > Linked Devices > Link a Device**, and scan the QR code displayed on the screen.
   * **Option B (Phone Pairing Code):** Enter your phone number in international format (e.g., `8801700000000` with country code, no `+` or spaces) and click **Get Code**. Go to Link a Device on your phone and tap **Link with phone number instead** to enter the 8-digit code.
4. Once paired, your bot is active and will automatically reply to incoming messages from your configured targets!
