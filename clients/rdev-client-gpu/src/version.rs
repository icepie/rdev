pub const VERSION: &str = match option_env!("RDEV_VERSION") {
    Some(version) => version,
    None => env!("CARGO_PKG_VERSION"),
};

pub fn client_version() -> String {
    format!("rs/{VERSION}")
}
