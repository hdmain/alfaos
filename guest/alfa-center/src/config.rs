use std::fs;
use std::path::PathBuf;

#[derive(Clone)]
pub struct CenterConfig {
    pub api_url: String,
    pub token: String,
}

impl CenterConfig {
    pub fn load() -> Self {
        let mut api_url = "http://192.168.122.1:7391".to_string();
        let mut token = String::new();

        for path in candidate_paths() {
            if let Ok(text) = fs::read_to_string(&path) {
                for line in text.lines() {
                    let line = line.trim();
                    if line.is_empty() || line.starts_with('#') {
                        continue;
                    }
                    if let Some((k, v)) = line.split_once('=') {
                        match k.trim() {
                            "API_URL" => api_url = v.trim().to_string(),
                            "TOKEN" => token = v.trim().to_string(),
                            _ => {}
                        }
                    }
                }
                break;
            }
        }

        // Env overrides for debugging
        if let Ok(v) = std::env::var("ALFA_CENTER_API") {
            api_url = v;
        }
        if let Ok(v) = std::env::var("ALFA_CENTER_TOKEN") {
            token = v;
        }

        Self { api_url, token }
    }
}

fn candidate_paths() -> Vec<PathBuf> {
    let mut paths = Vec::new();
    paths.push(PathBuf::from("/etc/alfaos/center.conf"));
    if let Ok(home) = std::env::var("HOME") {
        paths.push(PathBuf::from(home).join(".config/alfaos/center.conf"));
    }
    paths.push(PathBuf::from("center.conf"));
    paths
}
