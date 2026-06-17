use std::sync::mpsc;

use tracing::warn;

use crate::protocol::CustomInputAreas;

pub fn get_input_area(_no_gui: bool, sender: mpsc::Sender<CustomInputAreas>) {
    if let Err(err) = sender.send(CustomInputAreas::default()) {
        warn!("Failed to send default custom input areas: {err}");
    }
}
