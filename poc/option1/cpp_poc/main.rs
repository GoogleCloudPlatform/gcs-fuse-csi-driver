extern "C" {
    fn mock_encrypt();
}

fn main() {
    println!("Rust wrapper starting...");
    unsafe {
        mock_encrypt();
    }
    println!("Rust wrapper finished.");
}
