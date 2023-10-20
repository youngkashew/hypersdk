//! A basic ERC-721 compatible contract.
//! The program serves as a non-fungible token with the ability to mint and burn.
//! Only supports whole units with no decimal places.
//!
//! The NFT must support the common NFT metadata format.
//! This includes the name, symbol, and URI of the NFT.
use metadata::Nft;
use wasmlanche_sdk::{program::Program, public, state_keys, types::Address};

pub mod example;
pub mod metadata;

const NAME: &str = "My NFT";
const SYMBOL: &str = "MNFT";
const TOTAL_SUPPLY: u64 = 1;

/// The program storage keys.
#[state_keys]
enum StateKey {
    /// The total supply of the token. Key prefix 0x0.
    TotalSupply,
    /// The name of the token. Key prefix 0x1.
    Name,
    /// The symbol of the token. Key prefix 0x2.
    Symbol,
    /// Metadata of the token. Key prefix 0x3.
    Metadata,
    /// Balance of the NFT token by address. Key prefix 0x4(address).
    Balance(Address),
    /// Counter -- used to keep track of total NFTs minted. Key prefix 0x5.
    Counter,
    /// Owner -- used to keep track of the owner of each NFT. Key prefix 0x6.
    Owner,
}

/// Initializes the NFT with all required metadata.
/// This includes the name, symbol, image URI, owner, and total supply.
/// Returns true if the initialization was successful.
#[public]
pub fn init(program: Program) -> bool {
    // Set token name
    program
        .state()
        .store(StateKey::Name.to_vec(), &NAME.as_bytes())
        .expect("failed to store nft name");

    // Set token symbol
    program
        .state()
        .store(StateKey::Symbol.to_vec(), &SYMBOL.as_bytes())
        .expect("failed to store nft symbol");

    // Set total supply
    program
        .state()
        .store(StateKey::TotalSupply.to_vec(), &TOTAL_SUPPLY)
        .expect("failed to store total supply");

    true
}

/// Mints NFT tokens and sends them to the recipient.
#[public]
pub fn mint(program: Program, recipient: Address) -> bool {
    const MINT_AMOUNT: i64 = 1;

    let mut counter = program
        .state()
        .get::<i64, _>(StateKey::Counter.to_vec())
        .expect("failed to store balance");

    // Offset by 1 to set initial edition to 1
    counter += 1;

    assert!(
        counter <= TOTAL_SUPPLY as i64,
        "max supply for nft exceeded"
    );

    // Generate NFT metadata and persist to storage
    // Give each NFT a unique version
    let nft_metadata = Nft::default()
        .with_symbol(SYMBOL.to_string())
        .with_name(NAME.to_string())
        .with_uri("ipfs://my-nft.jpg".to_string());

    program
        .state()
        .store(StateKey::Metadata.to_vec(), &nft_metadata)
        .expect("failed to store nft metadata");

    let balance = program
        .state()
        .get::<i64, _>(StateKey::Balance(recipient).to_vec())
        .expect("failed to get balance");

    program
        .state()
        .store(
            StateKey::Balance(recipient).to_vec(),
            &(balance + MINT_AMOUNT),
        )
        .expect("failed to store balance");

    program
        .state()
        .store(
            StateKey::Balance(recipient).to_vec(),
            &(balance + MINT_AMOUNT),
        )
        .expect("failed to store balance");

    program
        .state()
        .store(StateKey::Owner.to_vec(), &recipient)
        .is_ok()
}

#[public]
pub fn burn(program: Program, from: Address) -> bool {
    const BURN_AMOUNT: i64 = 1;

    // Only the owner of the NFT can burn it
    let owner = program
        .state()
        .get::<Address, _>(StateKey::Owner.to_vec())
        .expect("failed to get owner");

    assert_eq!(owner, from, "only the owner can burn the nft");

    let balance = program
        .state()
        .get::<i64, _>(StateKey::Balance(from).to_vec())
        .expect("failed to get balance");

    assert!(
        BURN_AMOUNT <= balance,
        "amount burned must be less than or equal to the user balance"
    );

    let counter = program
        .state()
        .get::<i64, _>(StateKey::Counter.to_vec())
        .expect("failed to get counter");

    assert!(counter > 0, "cannot burn more nfts");

    // Burn the NFT by transferring it to the zero address
    program
        .state()
        .store(StateKey::Balance(from).to_vec(), &(balance - BURN_AMOUNT))
        .expect("failed to store new balance");

    // TODO move to a lazy static? Or move to the VM layer entirely
    let null_address = Address::new([0; 32]);
    program
        .state()
        .store(StateKey::Owner.to_vec(), &null_address)
        .is_ok()
}
