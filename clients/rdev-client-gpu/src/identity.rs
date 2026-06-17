use rand::RngExt;
use std::fmt::Write;

pub fn new_instance_id() -> String {
    let mut bytes = [0u8; 16];
    rand::rng().fill(&mut bytes);
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(out, "{byte:02x}");
    }
    out
}
