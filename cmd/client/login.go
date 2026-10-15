package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/network"
)

// loginConfig holds saved login credentials and display settings.
type loginConfig struct {
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	Remember   bool    `json:"remember"`
	ResWidth   int32   `json:"res_width,omitempty"`
	ResHeight  int32   `json:"res_height,omitempty"`
	WindowMode int     `json:"window_mode,omitempty"` // 0=windowed, 1=borderless, 2=fullscreen
	VSync      bool    `json:"vsync,omitempty"`
	FPSCap     int32   `json:"fps_cap,omitempty"` // 0=60 (default), 30, 60, 120, 999=unlimited
	ShowFPS    bool    `json:"show_fps,omitempty"`
	MaxZoom        float32 `json:"max_zoom,omitempty"`
	CamSensitivity float32 `json:"cam_sensitivity,omitempty"`
	InvertPitch    bool    `json:"invert_pitch,omitempty"`
	CamCollision   bool    `json:"cam_collision,omitempty"`
	MasterVolume float32 `json:"master_volume,omitempty"`
	SFXVolume    float32 `json:"sfx_volume,omitempty"`
	BGMVolume    float32 `json:"bgm_volume,omitempty"`
	ServerIP     string  `json:"server_ip,omitempty"`
	// Rules is "retail" or "sandstorm": the PS2 client the server is built
	// for (models.Rules). Empty means Sandstorm's rules on Sandstorm's
	// address and retail's everywhere else.
	Rules string `json:"rules,omitempty"`
	AssetsDir    string  `json:"assets_dir,omitempty"`
	ISOPath      string  `json:"iso_path,omitempty"`
	// The F3 report's credit name, and a report server other than the
	// default (report.go).
	ReportName   string `json:"report_name,omitempty"`
	ReportServer string `json:"report_server,omitempty"`
}

// saveDir is a server's save folder: the stand-in for the PS2 memory card's
// game folder (network.CardFolder), beside the binary under saves/<server>/.
// One per server, because each server's patcher fills its own.
func saveDir(serverIP string) string {
	base := "."
	if exePath, err := os.Executable(); err == nil {
		base = filepath.Dir(exePath)
	}
	server := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < ' ' {
			return '_'
		}
		return r
	}, serverIP)
	return filepath.Join(base, "saves", server, network.CardFolder)
}

func configPath() string {
	// Store config next to the binary for portability.
	if exePath, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exePath), "client.json")
	}
	return "client.json"
}

func loadLoginConfig() loginConfig {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return loginConfig{}
	}
	var cfg loginConfig
	if json.Unmarshal(data, &cfg) != nil {
		return loginConfig{}
	}
	return cfg
}

func saveLoginConfig(cfg loginConfig) {
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0700)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0600)
}

func deleteLoginConfig() {
	os.Remove(configPath())
}

// Login screen layout constants (shared between input and draw).
const (
	loginCloseY  = int32(6)
	loginCloseSz = int32(28)
	loginFieldW  = int32(280)
	loginFieldH  = int32(30)
	loginCheckSz = int32(18)
	loginBtnH    = int32(34)
)

// Dynamic login layout — positions computed from screen center.
func loginCloseX() int32 { return screenWidth - 36 }
func loginFieldX() int32 { return (screenWidth - loginFieldW) / 2 }
func loginTitleY() int32 { return screenHeight/2 - 120 }
func loginSubY() int32   { return screenHeight/2 - 85 }
func loginUserLblY() int32 { return screenHeight/2 - 50 }
func loginUserY() int32  { return screenHeight/2 - 30 }
func loginPassLblY() int32 { return screenHeight/2 + 20 }
func loginPassY() int32  { return screenHeight/2 + 40 }
func loginCheckY() int32 { return screenHeight/2 + 85 }
func loginBtnY() int32   { return screenHeight/2 + 115 }

// handleLoginInput processes keyboard and mouse input for the login screen.
func (c *Client) handleLoginInput() {
	if c.loginBusy {
		return
	}

	// Mouse clicks: close button, focus fields, toggle checkbox, login button
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		switch {
		case mx >= loginCloseX() && mx <= loginCloseX()+loginCloseSz && my >= loginCloseY && my <= loginCloseY+loginCloseSz:
			c.loginWantsExit = true
		case mx >= loginFieldX() && mx <= loginFieldX()+loginFieldW && my >= loginUserY() && my <= loginUserY()+loginFieldH:
			c.loginFocus = 0
		case mx >= loginFieldX() && mx <= loginFieldX()+loginFieldW && my >= loginPassY() && my <= loginPassY()+loginFieldH:
			c.loginFocus = 1
		case mx >= loginFieldX() && mx <= loginFieldX()+loginCheckSz+100 && my >= loginCheckY() && my <= loginCheckY()+loginCheckSz:
			c.loginFocus = 2
			// Toggle handled by uiCheckbox in drawLoginScreen
		case mx >= loginFieldX() && mx <= loginFieldX()+loginFieldW && my >= loginBtnY() && my <= loginBtnY()+loginBtnH:
			if len(c.loginUser) > 0 && len(c.loginPass) > 0 {
				c.attemptLogin()
			}
		}
	}

	// Tab or D-pad Down: cycle focus forward
	if rl.IsKeyPressed(rl.KeyTab) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.loginFocus = (c.loginFocus + 1) % 4 // 0=user, 1=pass, 2=remember, 3=login btn
	}
	// D-pad Up: cycle focus backward
	if gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp) {
		c.loginFocus = (c.loginFocus + 3) % 4
	}

	// Enter or Cross: advance field, toggle checkbox, or attempt login
	gpCross := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)
	if rl.IsKeyPressed(rl.KeyEnter) || gpCross {
		switch c.loginFocus {
		case 0: // username -> advance to password
			if len(c.loginUser) > 0 {
				c.loginFocus = 1
			}
		case 1: // password -> attempt login
			if len(c.loginUser) > 0 && len(c.loginPass) > 0 {
				c.attemptLogin()
			}
		case 2: // remember checkbox -> toggle
			c.loginRemember = !c.loginRemember
		case 3: // login button -> attempt login
			if len(c.loginUser) > 0 && len(c.loginPass) > 0 {
				c.attemptLogin()
			}
		}
		return
	}

	// Triangle: exit login
	if gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp) {
		c.loginWantsExit = true
		return
	}

	// Space on checkbox: toggle
	if c.loginFocus == 2 && rl.IsKeyPressed(rl.KeySpace) {
		c.loginRemember = !c.loginRemember
		return
	}

	// Text input only applies to username (0) and password (1) fields
	if c.loginFocus > 1 {
		return
	}

	// Backspace: delete last char from focused field
	if rl.IsKeyPressed(rl.KeyBackspace) {
		if c.loginFocus == 1 {
			if len(c.loginPass) > 0 {
				c.loginPass = c.loginPass[:len(c.loginPass)-1]
			}
		} else {
			if len(c.loginUser) > 0 {
				c.loginUser = c.loginUser[:len(c.loginUser)-1]
			}
		}
	}

	// Character input
	for ch := rl.GetCharPressed(); ch != 0; ch = rl.GetCharPressed() {
		if ch < 32 || ch > 126 {
			continue
		}
		if c.loginFocus == 1 {
			if len(c.loginPass) < 32 {
				c.loginPass += string(rune(ch))
			}
		} else {
			if len(c.loginUser) < 32 {
				c.loginUser += string(rune(ch))
			}
		}
	}
}

// loginResult is the auth goroutine's handoff to the main goroutine: exactly
// one is sent per attemptLogin. Keeping engine/loginBusy/loginError on the main
// goroutine (see pollLoginResult) avoids data races on those fields.
type loginResult struct {
	engine *game.Engine
	err    string
}

// attemptLogin runs TCP auth in a goroutine and delivers the result over
// c.loginResultCh. The main loop drains it via pollLoginResult — the goroutine
// never touches c.engine/loginBusy/loginError directly.
func (c *Client) attemptLogin() {
	c.loginBusy = true
	c.loginError = ""

	// Capture everything the goroutine needs up front — it must not read Client
	// fields concurrently with the main goroutine.
	user := c.loginUser
	pass := c.loginPass
	assetsDir := c.assetsDir
	isoPath := c.isoPath
	audio := c.audioSys
	serverIP := c.serverIP
	remember := c.loginRemember

	ch := make(chan loginResult, 1)
	c.loginResultCh = ch

	go func() {
		accountID, sessionToken, err := network.Login(serverIP, sandstormAuthPort, user, pass)
		if err != nil {
			log.Printf("Login failed: %v", err)
			ch <- loginResult{err: fmt.Sprintf("Login failed: %v", err)}
			return
		}

		log.Printf("Auth success: accountID=%d, token=%d bytes", accountID, len(sessionToken))

		// Save or delete config based on "Remember me" state.
		// Preserve display settings (resolution, window mode) regardless.
		existing := loadLoginConfig()
		if remember {
			existing.Username = user
			existing.Password = pass
			existing.Remember = true
			saveLoginConfig(existing)
		} else {
			existing.Username = ""
			existing.Password = ""
			existing.Remember = false
			saveLoginConfig(existing)
		}

		engine := game.NewEngineWithAuth(
			serverIP, sandstormGamePort, sandstormAuthPort, sandstormPatchPort,
			user, pass, accountID, sessionToken,
		)
		engine.AssetsDir = assetsDir
		engine.ISOPath = isoPath
		engine.SaveDir = saveDir(serverIP) // the patcher's card folder
		engine.Audio = audio
		engine.State.DebugEntityMovement = true

		ch <- loginResult{engine: engine}
	}()
}

// pollLoginResult applies a pending auth result (if any) on the main goroutine.
// Call it each frame while waiting for login. Non-blocking.
func (c *Client) pollLoginResult() {
	if c.loginResultCh == nil {
		return
	}
	select {
	case res := <-c.loginResultCh:
		c.loginResultCh = nil
		c.loginBusy = false
		if res.err != "" {
			c.loginError = res.err
		} else {
			c.engine = res.engine
		}
	default:
	}
}

// drawLoginScreen renders the username/password login form.
func (c *Client) drawLoginScreen() {
	gold := colorGoldWarm
	fieldBg := colorFieldBg
	focusBorder := colorGold
	unfocusBorder := rl.Color{R: 60, G: 60, B: 80, A: 255}

	// Dark backdrop behind login area — 20px padding around content
	const pad20 = int32(20)
	panelX := loginFieldX() - pad20
	panelY := loginSubY() - pad20
	panelW := loginFieldW + pad20*2
	panelH := (loginBtnY() + loginBtnH) - loginSubY() + pad20*2
	c.uiPanel(panelX, panelY, panelW, panelH)

	// Close button (top-right X)
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	closeHover := mx >= loginCloseX() && mx <= loginCloseX()+loginCloseSz && my >= loginCloseY && my <= loginCloseY+loginCloseSz
	closeBg := colorGmBtnBg
	closeColor := colorDim
	if closeHover {
		closeBg = rl.Color{R: 160, G: 40, B: 40, A: 255}
		closeColor = rl.White
	}
	rl.DrawRectangle(loginCloseX(), loginCloseY, loginCloseSz, loginCloseSz, closeBg)
	pad := int32(7)
	rl.DrawLine(loginCloseX()+pad, loginCloseY+pad, loginCloseX()+loginCloseSz-pad, loginCloseY+loginCloseSz-pad, closeColor)
	rl.DrawLine(loginCloseX()+pad, loginCloseY+loginCloseSz-pad, loginCloseX()+loginCloseSz-pad, loginCloseY+pad, closeColor)

	// Server name
	c.drawCenteredText("Sandstorm Server", loginSubY(), 16, gold)

	// Username label + field
	c.drawText("Username", loginFieldX(), loginUserLblY(), 16, rl.LightGray)
	c.drawLoginField(loginFieldX(), loginUserY(), loginFieldW, loginFieldH,
		c.loginUser, c.loginFocus == 0, false, fieldBg, focusBorder, unfocusBorder)

	// Password label + field
	c.drawText("Password", loginFieldX(), loginPassLblY(), 16, rl.LightGray)
	c.drawLoginField(loginFieldX(), loginPassY(), loginFieldW, loginFieldH,
		c.loginPass, c.loginFocus == 1, true, fieldBg, focusBorder, unfocusBorder)

	// "Remember me" checkbox
	c.uiCheckbox(loginFieldX(), loginCheckY(), "Remember me", &c.loginRemember)

	// Login button
	label := "Login"
	if c.loginBusy {
		label = "Authenticating..."
	}
	c.uiButtonStyled(loginFieldX(), loginBtnY(), loginFieldW, loginBtnH, label, 18)

	// Status message (errors only -- hint removed since we have a button now)
	if c.loginError != "" {
		c.drawCenteredText(c.loginError, loginBtnY()+loginBtnH+10, 14, rl.Color{R: 220, G: 60, B: 60, A: 255})
	}

	// Mute button (top-left)
	c.uiMuteButton(6, 6)
}

// drawLoginField renders a text input field with theme-aware styling.
func (c *Client) drawLoginField(x, y, w, h int32, text string, focused, isPassword bool,
	fieldBg, focusBorder, unfocusBorder rl.Color) {
	border := unfocusBorder
	if focused {
		border = focusBorder
	}

	displayText := text
	if isPassword {
		displayText = strings.Repeat("*", len(text))
	}
	if focused && int(time.Now().UnixMilli()/500)%2 == 0 {
		displayText += "|"
	}

	rl.DrawRectangle(x-2, y-2, w+4, h+4, border)
	rl.DrawRectangle(x, y, w, h, fieldBg)

	c.drawText(displayText, x+6, y+7, 16, rl.White)
}
