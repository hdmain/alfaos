mod api;
mod config;
mod display;

use eframe::egui::{self, Color32, RichText, Rounding, Stroke, Vec2};
use egui::{Frame, Margin};

use crate::api::{ApiClient, Status};
use crate::config::CenterConfig;
use crate::display::{apply_local, current_resolution, quality_geometry};

fn main() -> eframe::Result<()> {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_inner_size([720.0, 560.0])
            .with_min_inner_size([560.0, 420.0])
            .with_title("Alfa Center"),
        ..Default::default()
    };
    eframe::run_native(
        "Alfa Center",
        options,
        Box::new(|cc| {
            apply_black_theme(&cc.egui_ctx);
            Ok(Box::new(AlfaCenterApp::new()))
        }),
    )
}

fn apply_black_theme(ctx: &egui::Context) {
    let mut style = (*ctx.style()).clone();
    style.visuals.dark_mode = true;
    style.visuals.window_fill = Color32::from_rgb(8, 8, 8);
    style.visuals.panel_fill = Color32::from_rgb(8, 8, 8);
    style.visuals.extreme_bg_color = Color32::from_rgb(16, 16, 16);
    style.visuals.faint_bg_color = Color32::from_rgb(20, 20, 20);
    style.visuals.widgets.noninteractive.bg_fill = Color32::from_rgb(18, 18, 18);
    style.visuals.widgets.inactive.bg_fill = Color32::from_rgb(28, 28, 28);
    style.visuals.widgets.hovered.bg_fill = Color32::from_rgb(40, 40, 40);
    style.visuals.widgets.active.bg_fill = Color32::from_rgb(50, 50, 50);
    style.visuals.selection.bg_fill = Color32::from_rgb(140, 20, 30);
    style.visuals.override_text_color = Some(Color32::from_rgb(230, 230, 230));
    style.visuals.widgets.noninteractive.fg_stroke = Stroke::new(1.0, Color32::from_rgb(200, 200, 200));
    style.spacing.item_spacing = Vec2::new(10.0, 8.0);
    style.spacing.button_padding = Vec2::new(14.0, 8.0);
    ctx.set_style(style);
}

struct AlfaCenterApp {
    cfg: CenterConfig,
    api: ApiClient,
    status: Option<Status>,
    tab: Tab,
    quality: String,
    onion_on: bool,
    onion_stable: bool,
    idle_minutes: String,
    wake_on_rdp: bool,
    current_pw: String,
    new_pw: String,
    new_pw2: String,
    message: String,
    error: String,
    busy: bool,
    live_resolution: String,
}

#[derive(PartialEq)]
enum Tab {
    Connection,
    Privacy,
    Power,
    Password,
    Status,
}

impl AlfaCenterApp {
    fn new() -> Self {
        let cfg = CenterConfig::load();
        let api = ApiClient::new(cfg.api_url.clone(), cfg.token.clone());
        let mut app = Self {
            cfg,
            api,
            status: None,
            tab: Tab::Connection,
            quality: "high".into(),
            onion_on: false,
            onion_stable: false,
            idle_minutes: "15".into(),
            wake_on_rdp: true,
            current_pw: String::new(),
            new_pw: String::new(),
            new_pw2: String::new(),
            message: String::new(),
            error: String::new(),
            busy: false,
            live_resolution: current_resolution().unwrap_or_else(|_| "?".into()),
        };
        app.refresh();
        app
    }

    fn refresh_live_resolution(&mut self) {
        self.live_resolution = current_resolution().unwrap_or_else(|_| "?".into());
    }

    fn refresh(&mut self) {
        self.error.clear();
        match self.api.status() {
            Ok(st) => {
                self.quality = st.rdp_quality.clone();
                if self.quality == "custom" {
                    self.quality = "high".into();
                }
                self.onion_on = st.onioning;
                self.onion_stable = st.onioning_stable;
                self.idle_minutes = st.idle_shutdown_minutes.to_string();
                self.wake_on_rdp = st.wake_on_rdp;
                self.status = Some(st);
                self.message = "Connected to alfaos host API.".into();
            }
            Err(e) => {
                self.status = None;
                self.error = format!("Cannot reach alfaos API ({}): {}", self.cfg.api_url, e);
            }
        }
    }

    fn set_ok(&mut self, msg: impl Into<String>) {
        self.message = msg.into();
        self.error.clear();
    }

    fn set_err(&mut self, msg: impl Into<String>) {
        self.error = msg.into();
        self.message.clear();
    }
}

impl eframe::App for AlfaCenterApp {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        egui::TopBottomPanel::top("header")
            .frame(
                Frame::none()
                    .fill(Color32::from_rgb(0, 0, 0))
                    .inner_margin(Margin::symmetric(16.0, 12.0))
                    .stroke(Stroke::new(1.0_f32, Color32::from_rgb(40, 40, 40))),
            )
            .show(ctx, |ui| {
                ui.horizontal(|ui| {
                    ui.heading(
                        RichText::new("ALFA CENTER")
                            .color(Color32::from_rgb(200, 40, 50))
                            .strong()
                            .size(22.0),
                    );
                    ui.add_space(8.0);
                    ui.label(
                        RichText::new("settings for your ALFAOS desktop")
                            .color(Color32::from_rgb(120, 120, 120))
                            .size(13.0),
                    );
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        if ui.button("Refresh").clicked() {
                            self.refresh();
                        }
                    });
                });
            });

        egui::TopBottomPanel::bottom("footer")
            .frame(
                Frame::none()
                    .fill(Color32::BLACK)
                    .inner_margin(Margin::symmetric(16.0, 10.0)),
            )
            .show(ctx, |ui| {
                if !self.error.is_empty() {
                    ui.colored_label(Color32::from_rgb(220, 80, 80), &self.error);
                } else if !self.message.is_empty() {
                    ui.colored_label(Color32::from_rgb(80, 180, 100), &self.message);
                } else {
                    ui.label(RichText::new(" ").size(12.0));
                }
            });

        egui::SidePanel::left("nav")
            .exact_width(160.0)
            .frame(
                Frame::none()
                    .fill(Color32::from_rgb(6, 6, 6))
                    .inner_margin(Margin::same(12.0))
                    .stroke(Stroke::new(1.0_f32, Color32::from_rgb(30, 30, 30))),
            )
            .show(ctx, |ui| {
                ui.add_space(4.0);
                for (tab, label) in [
                    (Tab::Connection, "Connection"),
                    (Tab::Privacy, "Privacy"),
                    (Tab::Power, "Power"),
                    (Tab::Password, "Password"),
                    (Tab::Status, "Status"),
                ] {
                    let selected = self.tab == tab;
                    let text = if selected {
                        RichText::new(label).color(Color32::from_rgb(255, 80, 90)).strong()
                    } else {
                        RichText::new(label).color(Color32::from_rgb(180, 180, 180))
                    };
                    if ui
                        .add_sized(
                            [140.0, 32.0],
                            egui::SelectableLabel::new(selected, text),
                        )
                        .clicked()
                    {
                        self.tab = tab;
                    }
                    ui.add_space(2.0);
                }
            });

        egui::CentralPanel::default()
            .frame(
                Frame::none()
                    .fill(Color32::from_rgb(10, 10, 10))
                    .inner_margin(Margin::same(20.0)),
            )
            .show(ctx, |ui| {
                if self.busy {
                    ui.disable();
                }
                match self.tab {
                    Tab::Connection => self.ui_connection(ui),
                    Tab::Privacy => self.ui_privacy(ui),
                    Tab::Power => self.ui_power(ui),
                    Tab::Password => self.ui_password(ui),
                    Tab::Status => self.ui_status(ui),
                }
            });
    }
}

impl AlfaCenterApp {
    fn section_title(ui: &mut egui::Ui, title: &str, hint: &str) {
        ui.label(RichText::new(title).size(18.0).strong().color(Color32::WHITE));
        ui.label(RichText::new(hint).size(12.0).color(Color32::from_rgb(130, 130, 130)));
        ui.add_space(12.0);
    }

    fn ui_connection(&mut self, ui: &mut egui::Ui) {
        Self::section_title(
            ui,
            "Connection profile",
            "Optimizes RDP for your link speed (color depth, compression, lighter desktop). Display resolution can stay the same.",
        );

        ui.horizontal(|ui| {
            ui.label(
                RichText::new(format!("Display now: {}", self.live_resolution))
                    .color(Color32::from_rgb(140, 140, 140)),
            );
            if ui.small_button("↻").clicked() {
                self.refresh_live_resolution();
            }
        });
        ui.add_space(8.0);

        egui::ComboBox::from_label("Profile")
            .selected_text(match self.quality.as_str() {
                "low" => "Slow link — save bandwidth (more lag)",
                "medium" => "Balanced — wan",
                "high" => "LAN / fast — low lag + alfaoslite wallpaper",
                "ultra" => "Max — lowest lag + high res",
                other => other,
            })
            .show_ui(ui, |ui| {
                ui.selectable_value(
                    &mut self.quality,
                    "low".into(),
                    "Slow link — save bandwidth (more lag)",
                );
                ui.selectable_value(
                    &mut self.quality,
                    "medium".into(),
                    "Balanced — wan",
                );
                ui.selectable_value(
                    &mut self.quality,
                    "high".into(),
                    "LAN / fast — low lag + alfaoslite wallpaper",
                );
                ui.selectable_value(
                    &mut self.quality,
                    "ultra".into(),
                    "Max — lowest lag + high res",
                );
            });

        ui.add_space(16.0);
        if ui
            .add_sized(
                [260.0, 36.0],
                egui::Button::new(RichText::new("Apply profile").strong())
                    .fill(Color32::from_rgb(140, 20, 30))
                    .rounding(Rounding::same(4.0)),
            )
            .clicked()
        {
            self.busy = true;
            let q = self.quality.clone();
            match quality_geometry(&q) {
                None => self.set_err("Unknown profile"),
                Some((w, h, bpp)) => match apply_local(&q, w, h, bpp) {
                    Ok(local_msg) => {
                        self.refresh_live_resolution();
                        match self.api.set_quality(&q) {
                            Ok(api_msg) => {
                                self.set_ok(format!("{local_msg} | {api_msg}"));
                                self.refresh();
                            }
                            Err(e) => self.set_ok(format!(
                                "{local_msg} | host API warn: {e} (guest profile already applied)"
                            )),
                        }
                    }
                    Err(e) => self.set_err(e),
                },
            }
            self.busy = false;
        }

        ui.add_space(10.0);
        ui.label(
            RichText::new(
                "For less mouse lag pick LAN / fast, Apply, then reconnect RDP.\n\
                 Prefer direct VM IP (alfaos connect) — host :3389 proxy adds lag.\n\
                 Slow link saves bandwidth but increases input lag.",
            )
            .color(Color32::from_rgb(120, 120, 120))
            .size(12.0),
        );
    }

    fn ui_privacy(&mut self, ui: &mut egui::Ui) {
        Self::section_title(
            ui,
            "Onioning (Tor)",
            "Routes all VM internet through Tor on the host. RDP stays direct.",
        );

        ui.checkbox(&mut self.onion_on, "Enable onioning (Tor + killswitch)");
        ui.add_enabled_ui(self.onion_on, |ui| {
            ui.checkbox(
                &mut self.onion_stable,
                "Stable exit IP (~10 min rotation, same IP for all sites)",
            );
        });

        ui.add_space(16.0);
        if ui
            .add_sized(
                [200.0, 36.0],
                egui::Button::new(RichText::new("Apply privacy").strong())
                    .fill(Color32::from_rgb(140, 20, 30))
                    .rounding(Rounding::same(4.0)),
            )
            .clicked()
        {
            self.busy = true;
            match self.api.set_onioning(self.onion_on, self.onion_stable) {
                Ok(msg) => {
                    self.set_ok(msg);
                    self.refresh();
                }
                Err(e) => self.set_err(e),
            }
            self.busy = false;
        }
    }

    fn ui_power(&mut self, ui: &mut egui::Ui) {
        Self::section_title(
            ui,
            "Power saving",
            "Host can shut down the VM when idle and wake it on RDP connect.",
        );

        ui.horizontal(|ui| {
            ui.label("Idle shutdown (minutes, 0 = never)");
            ui.add(
                egui::TextEdit::singleline(&mut self.idle_minutes)
                    .desired_width(80.0)
                    .hint_text("15"),
            );
        });
        ui.checkbox(&mut self.wake_on_rdp, "Wake VM when an RDP client connects");

        ui.add_space(16.0);
        if ui
            .add_sized(
                [200.0, 36.0],
                egui::Button::new(RichText::new("Apply power").strong())
                    .fill(Color32::from_rgb(140, 20, 30))
                    .rounding(Rounding::same(4.0)),
            )
            .clicked()
        {
            match self.idle_minutes.trim().parse::<i32>() {
                Ok(mins) => {
                    self.busy = true;
                    match self.api.set_power(mins, self.wake_on_rdp) {
                        Ok(msg) => {
                            self.set_ok(msg);
                            self.refresh();
                        }
                        Err(e) => self.set_err(e),
                    }
                    self.busy = false;
                }
                Err(_) => self.set_err("Idle minutes must be a number"),
            }
        }
    }

    fn ui_password(&mut self, ui: &mut egui::Ui) {
        Self::section_title(
            ui,
            "Change password",
            "Updates RDP/SSH password in the VM and on the host config. Current password required.",
        );

        ui.label("Current password");
        ui.add(
            egui::TextEdit::singleline(&mut self.current_pw)
                .password(true)
                .desired_width(280.0),
        );
        ui.add_space(6.0);
        ui.label("New password");
        ui.add(
            egui::TextEdit::singleline(&mut self.new_pw)
                .password(true)
                .desired_width(280.0),
        );
        ui.add_space(6.0);
        ui.label("Confirm new password");
        ui.add(
            egui::TextEdit::singleline(&mut self.new_pw2)
                .password(true)
                .desired_width(280.0),
        );

        ui.add_space(16.0);
        if ui
            .add_sized(
                [220.0, 36.0],
                egui::Button::new(RichText::new("Change password").strong())
                    .fill(Color32::from_rgb(140, 20, 30))
                    .rounding(Rounding::same(4.0)),
            )
            .clicked()
        {
            if self.new_pw != self.new_pw2 {
                self.set_err("New passwords do not match");
            } else if self.new_pw.trim().is_empty() {
                self.set_err("New password cannot be empty");
            } else {
                self.busy = true;
                match self.api.change_password(&self.current_pw, &self.new_pw) {
                    Ok(msg) => {
                        self.set_ok(msg);
                        self.current_pw.clear();
                        self.new_pw.clear();
                        self.new_pw2.clear();
                    }
                    Err(e) => self.set_err(e),
                }
                self.busy = false;
            }
        }
    }

    fn ui_status(&mut self, ui: &mut egui::Ui) {
        Self::section_title(ui, "Status", "Live state from the alfaos host API.");

        if let Some(st) = &self.status {
            ui.monospace(format!("VM:            {}", st.vm_name));
            ui.monospace(format!(
                "Running:       {}",
                if st.vm_running { "yes" } else { "no" }
            ));
            ui.monospace(format!(
                "RDP:           {}×{} ({})",
                st.rdp_width, st.rdp_height, st.rdp_quality
            ));
            ui.monospace(format!(
                "Onioning:      {} (active={}, stable={})",
                st.onioning, st.onioning_active, st.onioning_stable
            ));
            ui.monospace(format!(
                "Idle shutdown: {} min",
                st.idle_shutdown_minutes
            ));
            ui.monospace(format!("Wake on RDP:   {}", st.wake_on_rdp));
            ui.monospace(format!("DNS:           {}", st.dns.join(", ")));
        } else {
            ui.label("No status — API unreachable.");
        }

        ui.add_space(12.0);
        ui.label(
            RichText::new(format!("API: {}", self.cfg.api_url))
                .color(Color32::from_rgb(100, 100, 100))
                .size(11.0),
        );
    }
}
