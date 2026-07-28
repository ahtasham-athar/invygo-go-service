# Magrabi EN voice — pronunciation dictionary worksheet

The Sara agent runs an **English** Cartesia voice, so Arabic names get anglicized ("Magrabi" → "may-GRAY-bee", "Naeim" → "naym"). Fix = a Cartesia pronunciation dictionary attached to the agent's TTS config. The dictionary works at the TTS layer, so it corrects the greeting, KB answers, and the live doctor names from `get_doctors` all at once — no prompt or DB changes.

## Setup — one shot via API

All entries are ready to POST in **`pronunciation_dictionary_magrabi.json`** (same folder). Create the whole dictionary in one call:

```bash
# from hospital-demo-service/  (Git Bash or any shell with curl)
curl -X POST https://api.cartesia.ai/pronunciation-dicts/ \
  -H "Cartesia-Version: 2026-03-01" \
  -H "Authorization: Bearer $CARTESIA_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @pronunciation_dictionary_magrabi.json
```

PowerShell equivalent:

```powershell
Invoke-RestMethod -Method Post -Uri "https://api.cartesia.ai/pronunciation-dicts/" `
  -Headers @{ "Cartesia-Version" = "2026-03-01"; "Authorization" = "Bearer $env:CARTESIA_API_KEY" } `
  -ContentType "application/json; charset=utf-8" `
  -Body ([System.IO.File]::ReadAllText("pronunciation_dictionary_magrabi.json", [System.Text.Encoding]::UTF8))
```

Then:

1. Grab the `id` (`pdict_…`) from the response.
2. ConvoiAI staging → Magrabi agent → TTS (Cartesia) options → add `pronunciation_dictionary_id: "<pdict_…>"`.
3. **Model must be sonic-3 or newer** — dictionaries silently don't apply on sonic-2. Check the agent's TTS model while you're in there.
4. Spot-check in the Cartesia playground: open the new dictionary and ▶-play a handful of entries (at least Magrabi, Naeim, Gamaly, Suud). The JSON was authored file-to-file so the IPA characters are intact, but the playground's ▶ button is the only ground truth for how the voice renders them — tweak any entry that sounds off there ("Regenerate phoneme" is a good starting point).
5. Test call: ask "who do you have at Abu Dhabi?" to force a run of live doctor names.

Pronunciation string format (per Cartesia docs): `<<ˈ|æ|k|m|i>>` — pipe-separated phonemes, `ˈ` = stress on the following syllable.

Keep the dictionary minimal — only entries the tester has verified by ear. An entry that "should" be right but sounds off on ▶ is worse than no entry.

## Tier 1 — flagged on the 27-Jul test call

| Word | English voice says | Target | IPA starting point |
|---|---|---|---|
| Magrabi | "may-GRAY-bee" / "MAG-ruh-by" | **muh-GRAH-bee** (stress GRAH) | /məˈɡrɑːbi/ |
| Naeim | "naym" | **nah-EEM** | /nɑːˈiːm/ |
| Mehrez | "MEER-ez" | **meh-REZ** | /mɛhˈrɛz/ |
| Gamaly | "GAM-uh-lee" | **gah-MAH-lee** (hard g — Egyptian) | /ɡɑːˈmɑːli/ |
| Afifi | "uh-FIFF-ee" | **ah-FEE-fee** | /ɑːˈfiːfi/ |
| Sabri | "SAY-bree" | **SAHB-ree** | /ˈsɑːbri/ |
| Suud | "sood" (one syllable) | **soo-OOD** (two syllables) | /suːˈuːd/ |

## Tier 2 — rest of the live roster (same risk class, any can come up in a call)

| Word | Target | IPA starting point |
|---|---|---|
| Hesham / Hisham | heh-SHAAM / hih-SHAAM | /hɛˈʃɑːm/ · /hɪˈʃɑːm/ |
| Aly | AH-lee (not "AY-lee") | /ˈɑːli/ |
| Moataz | MOH-ah-taz | /ˈmoʊɑːtæz/ |
| Sallam | sal-LAAM | /sɑːˈlɑːm/ |
| Lama | LAH-mah (not "llama"/"LAY-ma") | /ˈlɑːmɑː/ |
| ElJurdi | el-JOOR-dee | /ɛlˈdʒuːrdi/ |
| Amr | AH-mer | /ˈɑːmər/ |
| Faried | fah-REED | /fɑːˈriːd/ |
| Lotfi | LOT-fee | /ˈlɒtfi/ |
| Mounir | moo-NEER | /muːˈniːr/ |
| Tamer | TAH-mer (not "TAY-mer") | /ˈtɑːmər/ |
| Kamal | kah-MAAL | /kɑːˈmɑːl/ |
| Riham | ree-HAAM | /riːˈhɑːm/ |
| Hamdy | HAHM-dee | /ˈhɑːmdi/ |
| Wael | WAH-el (not "wale") | /ˈwɑːɛl/ |
| Manawy | mah-NAH-wee | /mɑːˈnɑːwi/ |
| Khaled | KAH-led | /ˈkɑːlɛd/ |
| Abdeen | ab-DEEN | /æbˈdiːn/ |
| Abdelhafez | ab-del-HAH-fez | /æbdɛlˈhɑːfɛz/ |
| Hasby | HAHS-bee | /ˈhɑːsbi/ |
| Ossama | oh-SAH-mah | /oʊˈsɑːmɑː/ |
| ElHakim | el-hah-KEEM | /ɛlhɑːˈkiːm/ |
| Kaoud | kah-OOD (not "cowed") | /kɑːˈuːd/ |
| Randa | RAHN-dah | /ˈrɑːndɑː/ |
| Kashif | KAH-shif | /ˈkɑːʃɪf/ |

Skipped as safe on English voices: Ahmed, Mohammad/Mohamed, Hassan, Hala, Omar, Morsi, Abu. Add them only if the tester flags one.

## Tier 3 — non-doctor words the agent speaks

| Word | Target | IPA starting point |
|---|---|---|
| Iqama | ih-KAH-mah | /ɪˈkɑːmɑː/ |
| Razy (Al Razy Building) | RAH-zee (not "RAY-zy") | /ˈrɑːzi/ |
| Khaleej | khah-LEEJ | /kɑːˈliːdʒ/ |
| Arabi | AH-rah-bee | /ˈɑːrɑbi/ |
| Mirdif | MIR-dif | /ˈmɪrdɪf/ |
| Hazaa | hah-ZAH | /hɑːˈzɑː/ |

Notes:
- English voices have no ح/خ/ع/ق — the targets above are the closest natural anglicizations, not strict Arabic. The goal is "sounds respectful and recognizable", not phonetically perfect.
- Multi-word names (Abu Suud, Al Razy, El Manawy) are matched per word — one entry per token.
- The LLM copies doctor names verbatim from `get_doctors`, so the written tokens are stable — the dictionary will match them.
