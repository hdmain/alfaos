use serde::Deserialize;
use serde_json::json;

#[derive(Debug, Clone, Deserialize)]
pub struct Status {
    pub ok: bool,
    pub vm_name: String,
    pub vm_running: bool,
    pub onioning: bool,
    pub onioning_stable: bool,
    pub onioning_active: bool,
    pub rdp_width: i32,
    pub rdp_height: i32,
    pub rdp_quality: String,
    pub idle_shutdown_minutes: i32,
    pub wake_on_rdp: bool,
    pub dns: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct ApiErr {
    error: String,
}

#[derive(Clone)]
pub struct ApiClient {
    base: String,
    token: String,
}

impl ApiClient {
    pub fn new(base: String, token: String) -> Self {
        Self { base, token }
    }

    fn request(&self, method: &str, path: &str, body: Option<serde_json::Value>) -> Result<String, String> {
        let url = format!("{}{}", self.base.trim_end_matches('/'), path);
        let mut req = match method {
            "GET" => ureq::get(&url),
            "POST" => ureq::post(&url),
            _ => return Err("unsupported method".into()),
        };
        req = req.set("X-Alfa-Token", &self.token).set("Content-Type", "application/json");

        let resp = if let Some(b) = body {
            req.send_json(b)
        } else {
            req.call()
        };

        match resp {
            Ok(r) => r.into_string().map_err(|e| e.to_string()),
            Err(ureq::Error::Status(code, r)) => {
                let text = r.into_string().unwrap_or_default();
                if let Ok(ae) = serde_json::from_str::<ApiErr>(&text) {
                    Err(format!("HTTP {code}: {}", ae.error))
                } else {
                    Err(format!("HTTP {code}: {text}"))
                }
            }
            Err(e) => Err(e.to_string()),
        }
    }

    pub fn status(&self) -> Result<Status, String> {
        let text = self.request("GET", "/api/status", None)?;
        serde_json::from_str(&text).map_err(|e| e.to_string())
    }

    pub fn set_quality(&self, quality: &str) -> Result<String, String> {
        let text = self.request(
            "POST",
            "/api/rdp/quality",
            Some(json!({ "quality": quality })),
        )?;
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&text) {
            let applied = v.get("applied").and_then(|x| x.as_bool()).unwrap_or(false);
            let w = v.get("width").and_then(|x| x.as_i64()).unwrap_or(0);
            let h = v.get("height").and_then(|x| x.as_i64()).unwrap_or(0);
            let bpp = v.get("bpp").and_then(|x| x.as_i64()).unwrap_or(0);
            let hint = v
                .get("hint")
                .and_then(|x| x.as_str())
                .unwrap_or("Reconnect RDP if the screen did not change.");
            if applied {
                return Ok(format!(
                    "Applied {quality} ({w}×{h}, {bpp}-bit). {hint}"
                ));
            }
            if let Some(warn) = v.get("warning").and_then(|x| x.as_str()) {
                return Ok(format!(
                    "Saved {quality} ({w}×{h}). Live resize: {warn}. Disconnect + reconnect RDP now."
                ));
            }
            return Ok(format!("Saved {quality} ({w}×{h}). {hint}"));
        }
        Ok(format!(
            "Quality set to {quality}. Disconnect and reconnect RDP to apply fully."
        ))
    }

    pub fn set_onioning(&self, enabled: bool, stable: bool) -> Result<String, String> {
        self.request(
            "POST",
            "/api/onioning",
            Some(json!({ "enabled": enabled, "stable": stable })),
        )?;
        Ok(if enabled {
            if stable {
                "Onioning ON (stable IP)".into()
            } else {
                "Onioning ON (privacy mode)".into()
            }
        } else {
            "Onioning OFF".into()
        })
    }

    pub fn set_power(&self, idle_minutes: i32, wake_on_rdp: bool) -> Result<String, String> {
        self.request(
            "POST",
            "/api/power",
            Some(json!({
                "idle_shutdown_minutes": idle_minutes,
                "wake_on_rdp": wake_on_rdp
            })),
        )?;
        Ok("Power settings saved.".into())
    }

    pub fn change_password(&self, current: &str, new_password: &str) -> Result<String, String> {
        self.request(
            "POST",
            "/api/password",
            Some(json!({ "current": current, "new": new_password })),
        )?;
        Ok("Password changed. Use it for the next RDP/SSH login.".into())
    }
}
