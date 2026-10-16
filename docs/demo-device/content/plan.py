#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The demo device's sample library: every photo, its date, place, people and prompt.
# Everything here is made up - the family, their home and their friends don't exist.
# `python3 plan.py OUT` writes OUT/prompts/<id>.txt (the full prompt sent for each image)
# and OUT/prompts/plan.json (what generate.py and build.py read).
import datetime as dt, json, os, sys

# ---------------------------------------------------------------- the family
BIRTH = {"Lucía": dt.date(1988, 5, 2), "Tom": dt.date(1986, 2, 9), "Nora": dt.date(2016, 12, 5),
         "Leo": dt.date(2021, 3, 14), "Rosa": dt.date(1958, 6, 30), "Jan": dt.date(1956, 9, 12)}
DISPLAY = {"Lucía": "Lucía", "Tom": "Tom", "Nora": "Nora", "Leo": "Leo", "Rosa": "Grandma Rosa", "Jan": "Grandpa Jan"}

def age(name, day):
    b = BIRTH[name]
    return day.year - b.year - ((day.month, day.day) < (b.month, b.day))

def describe(name, day):
    a = age(name, day)
    an = "an" if a in (8, 11, 18) or 80 <= a <= 89 else "a"
    return {
        "Lucía": f"Lucía, a {a}-year-old Spanish woman with shoulder-length wavy dark-brown hair, warm brown eyes and olive skin with light freckles",
        "Tom": f"Tom, a {a}-year-old Dutch man with short sandy-brown hair, a short trimmed beard and blue-grey eyes",
        "Nora": f"Nora, {an} {a}-year-old girl with long light-brown hair, hazel eyes and freckles",
        "Leo": f"Leo, {an} {a}-year-old boy with straight dark-brown hair in a short fringe, a broad round face, "
               f"thick dark eyebrows, dark-brown eyes and small round glasses with thin dark-blue frames (he always "
               f"wears them)" if a >= 3 else
               f"Leo, a {a}-year-old toddler (nearly 3) with straight dark-brown hair in a short fringe, a broad round "
               f"face, thick dark eyebrows, dark-brown eyes and small round glasses with thin dark-blue frames (he "
               f"always wears them)",
        "Rosa": f"Grandma Rosa, a {a}-year-old Italian woman with a short silver-grey bob and round tortoiseshell glasses",
        "Jan": f"Grandpa Jan, a {a}-year-old tall Dutch man, bald on top with short white hair at the sides, bushy white eyebrows and blue eyes",
    }[name]

# Which reference portrait stands for whom on a given day: the children have two, two years apart.
def ref_key(name, day):
    if name == "Nora":
        return "nora7" if day < dt.date(2025, 7, 1) else "nora9"
    if name == "Leo":
        return "leo3" if day < dt.date(2025, 7, 1) else "leo5"
    return {"Lucía": "lucia", "Tom": "tom", "Rosa": "rosa", "Jan": "jan"}[name]

REF_AGE = {"nora7": "aged 7", "nora9": "aged 9", "leo3": "aged 3", "leo5": "aged 5"}

STYLE_PHONE = ("Candid, natural photo as if taken with a good modern phone by a family member; realistic light, "
               "true-to-life colours, natural composition, natural skin texture, not retouched, not a studio shot. "
               "No text, no watermark, no border, no frame, not a collage.")
STYLE_CAMERA = ("Candid, natural photo as if taken by a parent with a good mirrorless camera; realistic light, "
                "true-to-life colours, natural composition, natural skin texture, not retouched. "
                "No text, no watermark, no border, no frame, not a collage.")
FACES = ("Every person's face is clearly visible, sharp and well lit, turned towards the camera, "
         "and large enough to recognise; normal hands with five fingers.")

# ---------------------------------------------------------------- places (real coordinates)
P = {
    "home": (52.0908, 5.1216),            # Utrecht, Domplein: a public square standing in for the family's home
    "grandparents": (52.3813, 4.6366),    # Haarlem, Grote Markt: standing in for Rosa and Jan's house
    "wilhelminapark": (52.0866, 5.1385), "amelisweerd": (52.0805, 5.1755), "betuwe": (51.8460, 5.3970),
    "zandvoort": (52.3740, 4.5260), "lisse": (52.2700, 4.5460), "bunnik": (52.0560, 5.2050),
    "trastevere": (41.8894, 12.4697), "trastevere_dinner": (41.8889, 12.4689), "colosseum": (41.8902, 12.4922),
    "forum": (41.8925, 12.4853), "navona": (41.8990, 12.4731), "ponte_sisto": (41.8923, 12.4707),
    "braies": (46.6943, 12.0857), "cortina_camp": (46.5560, 12.1340), "tre_cime": (46.6186, 12.3020),
    "seceda": (46.6000, 11.7240),
    "cabo_da_roca": (38.7804, -9.4989), "lagos_old_town": (37.1020, -8.6730), "ponta_da_piedade": (37.0808, -8.6693),
    "camilo": (37.0880, -8.6696), "meia_praia": (37.1110, -8.6880), "benagil": (37.0874, -8.4258),
    "benagil_sea": (37.0866, -8.4240), "carvoeiro": (37.0905, -8.4590), "lagos_marina": (37.1090, -8.6780),
    "luz": (37.0890, -8.7240), "porto_de_mos": (37.0855, -8.6880),
    "penken": (47.1830, 11.8370), "penken_school": (47.1850, 11.8420), "ahorn": (47.1550, 11.8780),
    "hintertux": (47.0750, 11.6700), "hintertux_hut": (47.0790, 11.6760), "penken_hut": (47.1830, 11.8400),
    "mayrhofen": (47.1667, 11.8650),
}

# ---------------------------------------------------------------- the shots
# id, local time, UTC offset, place key (None = no GPS), source, people, model, aspect, scene, description
# source: "phone" (Lucía's phone) or "camera:<Event folder>" (Tom's camera) or "reused:NN" (docs/promo sample).
# dog=True passes Biscuit's reference photo too.
S = []
def shot(id, when, off, place, src, people, scene, desc, model="flash", aspect=None, dog=False, refs=None, faces=True):
    S.append(dict(id=id, when=when, off=off, place=place, src=src, people=people, scene=scene, desc=desc, faces=faces,
                  model=model, aspect=aspect or ("3:2" if src.startswith("camera") else "4:3"), dog=dog, refs=refs))

# Reference portraits (Nano Banana Pro); they double as library photos.
PORTRAITS = {
    "lucia": dict(when="2026-05-10 10:42:18", off="+02:00", place="home", refs=[], scene=
        "A natural portrait photo of Lucía, a 38-year-old Spanish woman with shoulder-length wavy dark-brown hair, "
        "warm brown eyes, olive skin with a few light freckles across her nose and small silver hoop earrings, wearing "
        "a soft sage-green linen shirt. She stands in a leafy back garden in open shade on a spring morning, facing the "
        "camera straight on, head and shoulders, with a relaxed, genuine smile. Soft natural light on her face, sharp "
        "focus on the eyes, background softly blurred.", desc="Lucía in the garden on Mother's Day morning"),
    "tom": dict(when="2026-06-21 18:15:40", off="+02:00", place="home", refs=[], scene=
        "A natural portrait photo of Tom, a 40-year-old Dutch man with short sandy-brown hair, a short neatly trimmed "
        "beard, blue-grey eyes and fair skin with a few smile lines, wearing a navy crew-neck jumper over a white T-shirt. "
        "He sits on a garden bench in warm early-evening light, facing the camera straight on, head and shoulders, "
        "smiling with his eyes. Sharp focus on the eyes, background softly blurred.", desc="Tom on Father's Day"),
    "nora9": dict(when="2026-06-06 16:20:05", off="+02:00", place="wilhelminapark", refs=["lucia", "tom"], scene=
        "Reference photo 1 is Lucía and reference photo 2 is Tom. Make a natural portrait photo of their daughter Nora, "
        "a 9-year-old girl who clearly resembles both of them: long light-brown hair in a loose side braid, hazel eyes, "
        "light olive skin with freckles across her nose, a wide grin, wearing a mustard-yellow T-shirt. She stands in a "
        "green park in the afternoon shade, facing the camera straight on, head and shoulders. Soft natural light on her "
        "face, sharp focus on the eyes, background softly blurred.", desc="Nora in Wilhelminapark"),
    # Leo's first portraits were made with Nora's as a reference "so they resemble each other", and
    # SFace then scored the two children 0.5-0.65 - one person to the device. These use only the
    # parents' descriptions (no reference photo) and give him features clearly unlike his sister's.
    "leo5": dict(when="2026-04-04 11:05:51", off="+02:00", place="home", refs=[], scene=
        "A natural portrait photo of Leo, a 5-year-old Spanish-Dutch boy: thick, straight dark-brown hair cut in a short fringe across his forehead, a broad, round face "
        "with full cheeks, a wide button nose, deep-set dark-brown eyes under thick, straight dark eyebrows, small round "
        "glasses with thin dark-blue frames, olive skin like his mother's and no freckles, and a calm, closed-mouth "
        "smile, wearing a red-and-navy striped T-shirt. He "
        "stands in a back garden in soft morning light, facing the camera straight on, head and shoulders. Sharp focus "
        "on the eyes, background softly blurred.", desc="Leo in the garden, just turned five"),
    "rosa": dict(when="2026-07-19 15:30:12", off="+02:00", place="grandparents", refs=["tom"], scene=
        "Reference photo 1 is Tom. Make a natural portrait photo of his mother, Grandma Rosa, a 68-year-old Italian "
        "woman with a gentle family resemblance to him: a short silver-grey bob, round tortoiseshell glasses, brown eyes, "
        "warm olive skin with laugh lines and small pearl earrings, wearing a coral cardigan. She sits in her garden in "
        "summer shade, facing the camera straight on, head and shoulders, with a warm smile. Sharp focus on the eyes, "
        "background softly blurred.", desc="Grandma Rosa in her garden"),
    "jan": dict(when="2026-07-19 15:34:47", off="+02:00", place="grandparents", refs=["tom"], scene=
        "Reference photo 1 is Tom. Make a natural portrait photo of his father, Grandpa Jan, a 70-year-old tall Dutch "
        "man with a family resemblance to him: bald on top with short white hair at the sides, bushy white eyebrows, "
        "clean-shaven, bright blue eyes and a ruddy, weathered complexion, wearing a blue-and-green checked flannel "
        "shirt. He stands in his garden in summer shade, facing the camera straight on, head and shoulders, with a "
        "kind smile. Sharp focus on the eyes, background softly blurred.", desc="Grandpa Jan in his garden"),
    "nora7": dict(when="2024-03-30 15:10:33", off="+01:00", place="home", refs=["nora9"], scene=
        "Reference photo 1 is Nora at 9 years old. Make a natural portrait photo of the same girl two years younger, "
        "aged 7: clearly the same face, eyes, freckles and hair colour, but younger and a little rounder in the face, "
        "her light-brown hair shoulder-length in two low pigtails, wearing a "
        "pink raincoat open over a white top. She stands in a back garden under white spring blossom, facing the camera "
        "straight on, head and shoulders, with a happy closed-mouth smile. Soft overcast light, sharp focus on the eyes.", desc="Nora under the blossom"),
    "leo3": dict(when="2024-03-23 11:48:09", off="+01:00", place="home", refs=["leo5"], scene=
        "Reference photo 1 is Leo at 5 years old. Make a natural portrait photo of the same boy at 3 years old: clearly "
        "the same face, deep-set dark-brown eyes, thick straight dark eyebrows, straight dark-brown hair with a fringe "
        "and the same small round glasses with thin dark-blue frames, but a rounder toddler face and softer, shorter "
        "hair, wearing a green jumper with a little dinosaur on it. He sits on "
        "a garden lawn in soft spring light, facing the camera straight on, head and shoulders, with a big toddler smile. "
        "Sharp focus on the eyes, background softly blurred.", desc="Leo, just turned three"),
}
PORTRAIT_PEOPLE = {"lucia": "Lucía", "tom": "Tom", "nora9": "Nora", "leo5": "Leo", "rosa": "Rosa", "jan": "Jan",
                   "nora7": "Nora", "leo3": "Leo"}

BISCUIT = ("a golden retriever called Biscuit, a young adult dog with a light golden, slightly wavy coat, a soft "
           "friendly face and a red collar")

# Biscuit's own reference photo (a library photo too).
shot("biscuit", "2024-03-09 14:22:31", "+01:00", "home", "phone", [],
     f"{BISCUIT[0].upper() + BISCUIT[1:]}, lying on a back-garden lawn in early spring, head up, looking straight at the camera with his tongue "
     "out, soft afternoon light.", "Biscuit on the lawn", dog=False)

# -- 2024
# The first photo of the children in the timeline shows only Leo: with both of their faces in it, the
# device's matching (which only knows a person once one of their faces is stored) put the second
# child into the first one's person before either had one of their own, and they stayed merged.
shot("s01", "2024-01-14 10:21:44", "+01:00", "home", "phone", ["Leo"],
     "Leo, in a woolly hat and a colourful snowsuit, kneeling proudly beside the small snowman he has just built with "
     "his sister in a snowy back garden on a bright winter morning, laughing towards the camera; he is the only "
     "person in the photo.", "Leo's first snowman of the winter")
shot("s02", "2024-02-11 15:40:12", "+01:00", "home", "phone", ["Leo"],
     f"Leo cuddled up on a grey living-room sofa with {BISCUIT}, whose head rests on Leo's lap; Leo grins at the camera, "
     "soft afternoon window light, a blanket and cushions.", "Leo and Biscuit on the sofa", dog=True)
shot("s03", "2024-02-24 09:35:27", "+01:00", "home", "phone", ["Lucía", "Nora"],
     "Lucía and Nora making pancakes in a bright family kitchen on a weekend morning; Nora holds the pan and Lucía "
     "stands beside her, both laughing towards the camera, a plate of pancakes and a bowl of batter on the counter.",
     "Saturday pancakes")
# Rome, April 2024
shot("s04", "2024-04-19 17:10:48", "+02:00", "trastevere", "phone", ["Lucía", "Nora"],
     "Lucía and Nora eating gelato on a cobbled street in Trastevere, Rome, ochre houses covered in ivy behind them and "
     "a scooter parked by a door; both smile at the camera, warm late-afternoon light.", "Gelato in Trastevere")
shot("s05", "2024-04-19 20:45:03", "+02:00", "trastevere_dinner", "phone", [],
     "Dinner on a trattoria terrace in Trastevere, Rome: a plate of cacio e pepe, golden supplì and a glass of red wine "
     "on a red-and-white checked tablecloth, warm evening light and string lights blurred behind, seen from the diner's seat.",
     "Cacio e pepe and supplì in Trastevere")
shot("s06", "2024-04-20 09:15:22", "+02:00", "colosseum", "camera:Rome", [],
     "The Colosseum in Rome in soft morning light under a pale blue spring sky, seen from the street, a few tourists "
     "small in the distance. Travel landscape photo.", "The Colosseum in the morning")
shot("s07", "2024-04-20 09:41:10", "+02:00", "colosseum", "camera:Rome", ["Lucía", "Tom", "Nora", "Leo"],
     "A family photo in front of the Colosseum in Rome: Tom with little Leo sitting on his shoulders, Lucía and Nora "
     "standing next to them - all four of them in the frame - smiling at the camera, medium shot from the waist up so "
     "their faces are large, "
     "the Colosseum behind them in soft morning light.", "The four of us at the Colosseum")
shot("s08", "2024-04-20 11:32:51", "+02:00", "forum", "camera:Rome", [],
     "The Roman Forum seen from above: ancient columns, arches and ruins among umbrella pines, bright spring sky, no "
     "people in the foreground. Travel landscape photo.", "The Roman Forum")
shot("s09", "2024-04-20 16:20:37", "+02:00", "navona", "camera:Rome", ["Tom", "Leo"],
     "Tom crouching next to Leo at the edge of a baroque fountain in Piazza Navona, Rome; Leo points at the water and "
     "both laugh, faces turned three-quarters towards the camera, sunny afternoon, the square softly blurred behind.",
     "Leo and Tom at the fountain in Piazza Navona")
shot("s10", "2024-04-20 19:55:16", "+02:00", "ponte_sisto", "phone", ["Lucía", "Tom"],
     "A selfie of Lucía and Tom, cheek to cheek, on Ponte Sisto in Rome at sunset, the dome of St Peter's and the river "
     "Tiber glowing behind them, warm golden light on their faces, Tom's arm reaching out of the frame.",
     "Sunset selfie on Ponte Sisto", aspect="3:4")
# Spring 2024
shot("s12", "2024-05-12 14:50:02", "+02:00", "wilhelminapark", "phone", ["Jan", "Leo"],
     "Grandpa Jan walking beside Leo, who rides a small wooden balance bike along a park path in spring, both smiling "
     "at the camera, fresh green trees and dappled sunlight.", "Leo's balance bike with Grandpa Jan")
shot("s13", "2024-05-26 11:20:39", "+02:00", "home", "phone", ["Lucía", "Leo"],
     "Lucía kneeling by a wooden raised bed in a small back garden, planting flowers with Leo, who holds a little "
     "trowel; both look up at the camera and smile, soft late-spring light.", "Planting the flower bed")
shot("s14", "2024-06-23 19:31:55", "+02:00", "amelisweerd", "phone", [],
     f"{BISCUIT[0].upper() + BISCUIT[1:]}, trotting through a meadow of long grass with a stick in his mouth, golden evening light, trees behind. "
     "Pet photo, no people.", "Biscuit and the stick", dog=True)
# Dolomites, July 2024
shot("s15", "2024-07-21 10:40:13", "+02:00", "braies", "phone", ["Lucía", "Nora"],
     "Lucía and Nora sitting in a wooden rowing boat on the turquoise water of Lago di Braies in the Dolomites, "
     "mountains and pine forest behind, Lucía holding the oars; both smile at the camera, bright summer morning.",
     "Rowing on Lago di Braies")
shot("s16", "2024-07-22 08:05:41", "+02:00", "cortina_camp", "phone", ["Tom", "Leo"],
     "Morning at a mountain campsite in the Dolomites: Tom making coffee on a small camping stove in front of a green "
     "tent while Leo, in pyjamas and a fleece, sits in a little camping chair beside him; both look at the camera "
     "and smile, jagged peaks lit by the morning sun behind.", "Coffee at the campsite")
shot("s17", "2024-07-23 13:10:29", "+02:00", "tre_cime", "camera:Dolomites", ["Tom", "Leo"],
     "Tom hiking on a rocky trail with Leo in a child-carrier backpack, both faces visible and smiling at the camera, "
     "the three peaks of the Tre Cime di Lavaredo behind them, bright midday sun and a few clouds.",
     "Hiking at the Tre Cime with Leo in the carrier")
shot("s18", "2024-07-24 12:30:08", "+02:00", "seceda", "camera:Dolomites", [],
     "The Seceda ridgeline in the Dolomites: steep green meadows rising to a row of jagged rock peaks, a few wooden "
     "huts on the slopes, summer clouds. Landscape photo, no people.", "The Seceda ridge")
shot("s20", "2024-07-25 21:10:34", "+02:00", "cortina_camp", "phone", ["Nora", "Leo"],
     "Nora and Leo toasting marshmallows on sticks over a small campfire at dusk, their faces lit warm and clearly "
     "visible by the fire, looking at the camera and laughing, blue twilight mountains behind.", "Marshmallows by the fire")
shot("s22", "2024-08-18 18:40:20", "+02:00", "home", "phone", ["Tom", "Rosa", "Jan"],
     "A summer evening barbecue in a back garden: Tom at the grill holding tongs, Grandma Rosa and Grandpa Jan sitting "
     "at the garden table with drinks; all three laugh towards the camera, warm low sun.", "Barbecue with Rosa and Jan")
shot("s23", "2024-09-14 11:45:16", "+02:00", "betuwe", "phone", ["Tom", "Nora"],
     "Nora sitting on Tom's shoulders in an apple orchard, reaching up to pick a red apple; both faces visible, smiling "
     "at the camera, sunny early-autumn day, rows of apple trees.", "Apple picking", aspect="3:4")
shot("s25", "2024-10-26 16:10:42", "+02:00", "amelisweerd", "phone", ["Lucía"],
     f"Lucía kneeling on a forest path covered in autumn leaves, hugging {BISCUIT}; both look at the camera, Lucía "
     "smiling, golden afternoon light through the trees.", "Autumn walk with Biscuit", dog=True)
shot("s26", "2024-11-10 14:30:57", "+01:00", "grandparents", "phone", ["Rosa", "Nora"],
     "In a cosy kitchen, Grandma Rosa teaching Nora to weave a lattice crust on an apple pie, flour on the wooden table; "
     "both look up at the camera and smile, soft window light.", "Learning Grandma Rosa's apple pie")
# Christmas 2024 at the grandparents'
shot("s27", "2024-12-24 17:30:25", "+01:00", "grandparents", "phone", ["Jan", "Nora", "Leo"],
     "Grandpa Jan sitting on a sofa next to a lit Christmas tree, reading a picture book to Nora and Leo, who are tucked "
     "in on either side of him; all three look up at the camera and smile, warm lamp light.", "Christmas Eve story with Grandpa Jan")
shot("s28", "2024-12-24 19:45:38", "+01:00", "grandparents", "phone", ["Lucía", "Nora", "Leo", "Rosa", "Jan"],
     "Christmas Eve dinner: Lucía, Nora, Leo, Grandma Rosa and Grandpa Jan around a candlelit table with a festive meal, "
     "raising their glasses towards the camera and smiling, warm candle light, a Christmas tree in the corner.",
     "Christmas Eve dinner at Rosa and Jan's")
shot("s29", "2024-12-25 09:10:14", "+01:00", "grandparents", "phone", ["Leo"],
     "Leo in Christmas pyjamas tearing open a present under the Christmas tree, wrapping paper everywhere, looking up at "
     "the camera with a huge excited face, morning light.", "Leo's Christmas morning")
shot("s30", "2024-12-25 12:20:46", "+01:00", "grandparents", "phone", ["Lucía", "Rosa"],
     "Lucía and Grandma Rosa in a warm kitchen, Rosa in oven gloves lifting a bubbling lasagna out of the oven, both "
     "laughing towards the camera, Christmas decorations on the shelves.", "Grandma Rosa's Christmas lasagna")
shot("s32", "2024-12-26 15:30:09", "+01:00", "grandparents", "phone", ["Tom", "Nora"],
     "Tom and Nora in winter coats and knitted hats at a Christmas market in an old Dutch town square, holding paper "
     "cups of hot chocolate, string lights and stalls blurred behind; both smile at the camera, late afternoon.",
     "Christmas market in Haarlem", aspect="3:4")
# -- 2025
shot("s33", "2025-01-18 14:20:11", "+01:00", "wilhelminapark", "phone", ["Tom", "Nora"],
     "Tom holding Nora's hands as she ice-skates on a frozen park pond on a sunny winter afternoon: a medium shot, "
     "Nora tall for her age with her long light-brown hair in two braids under a knitted hat, both faces large, visible "
     "and laughing towards the camera, bare trees behind.", "Skating on the frozen pond")
shot("s34", "2025-02-08 10:30:52", "+01:00", "home", "phone", ["Tom", "Leo"],
     "Tom and Leo lying on a living-room rug building a tall tower of wooden blocks; Leo grins at the camera and Tom "
     "smiles, soft morning window light.", "The tallest tower")
shot("s36", "2025-03-22 15:15:07", "+01:00", "wilhelminapark", "phone", ["Nora"],
     f"Nora sitting under a blossoming cherry tree in a park, hugging {BISCUIT}; Nora smiles at the camera, pink petals "
     "on the grass, soft spring light.", "Nora and Biscuit under the cherry blossom", dog=True)
shot("s37", "2025-04-20 11:00:35", "+02:00", "grandparents", "phone", ["Rosa", "Leo"],
     "An Easter egg hunt in a spring garden: Leo holding a little basket of colourful painted eggs, Grandma Rosa crouching "
     "beside him and pointing at another egg in the grass; both faces visible and smiling at the camera.", "Easter egg hunt")
shot("s39", "2025-05-11 08:40:28", "+02:00", "home", "phone", ["Lucía", "Nora", "Leo"],
     "Mother's Day breakfast in bed: Lucía sitting up in bed, laughing, while Nora and Leo carry in a tray with orange "
     "juice, toast and a small vase of flowers; all three faces visible, soft morning light from the window.",
     "Mother's Day breakfast in bed")
shot("s40", "2025-04-26 12:10:44", "+02:00", "lisse", "phone", [],
     "Tulip fields in full bloom in the Netherlands: long straight rows of red, yellow and pink tulips under a big sky "
     "with white clouds, a farmhouse far away. Landscape photo, no people.", "Tulip fields near Lisse")
# Nora's school play, June 2025
shot("s41", "2025-06-19 19:05:19", "+02:00", None, "phone", ["Nora"],
     "Nora on a primary-school stage in a simple home-made sunflower costume (a green dress and yellow felt petals framing "
     "her face), singing in a spotlight, her face clearly visible and lit, other children softly blurred behind.",
     "Nora as the sunflower in the school play")
shot("s43", "2025-06-19 19:20:02", "+02:00", None, "phone", ["Rosa", "Jan"],
     "Grandma Rosa and Grandpa Jan sitting in the audience of a school hall, clapping and beaming towards the camera, "
     "other parents around them softly blurred, warm hall light.", "Rosa and Jan in the audience")
shot("s44", "2025-06-19 20:05:47", "+02:00", None, "phone", ["Lucía", "Tom", "Nora"],
     "After the school play: Nora, still in her sunflower costume, holding a small bouquet of flowers between Lucía and "
     "Tom in a school hallway with children's paintings on the walls; all three smile at the camera.",
     "After the show")
shot("s45", "2025-07-05 15:40:31", "+02:00", "home", "phone", ["Nora", "Leo"],
     "Nora and Leo splashing in a paddling pool in a sunny back garden, a garden sprinkler arcing water over them, both "
     "laughing at the camera, summer afternoon.", "Paddling pool afternoon")
# Algarve, August 2025
shot("s46", "2025-08-10 11:20:16", "+01:00", "lagos_old_town", "phone", ["Lucía", "Nora"],
     "Lucía and Nora walking hand in hand along a whitewashed cobbled street in Lagos old town, Portugal, pink "
     "bougainvillea over a doorway; both smile at the camera, bright summer morning.", "Lagos old town", aspect="3:4")
shot("s47", "2025-08-10 18:40:51", "+01:00", "ponta_da_piedade", "camera:Algarve", [],
     "Ponta da Piedade in the Algarve: golden limestone sea stacks and arches rising from turquoise water, seen from the "
     "cliff top in warm evening light. Landscape photo, no people.", "Ponta da Piedade")
shot("s48", "2025-08-11 10:15:33", "+01:00", "camilo", "phone", ["Lucía", "Leo"],
     "Beach day at a small cove in the Algarve: Leo digging in the sand with a red spade, Lucía sitting next to him under "
     "a striped beach umbrella; both faces visible, smiling at the camera, golden cliffs and turquoise sea behind.",
     "Digging at Praia do Camilo")
shot("s49", "2025-08-11 12:30:22", "+01:00", "meia_praia", "camera:Algarve", ["Tom", "Leo"],
     "Tom and Leo holding hands and jumping a small wave at the edge of a long sandy beach in the Algarve, splashes "
     "frozen in the air, both laughing towards the camera, bright midday sun.", "Jumping waves at Meia Praia")
shot("s50", "2025-08-13 10:40:09", "+01:00", "benagil", "camera:Algarve", [],
     "Inside the Benagil sea cave in the Algarve: a beam of sunlight falling through the round opening in the cave roof "
     "onto a small sandy beach, golden rock arches and turquoise water. Landscape photo, no people.", "Benagil cave")
shot("s51", "2025-08-13 10:05:44", "+01:00", "benagil_sea", "phone", ["Tom", "Nora"],
     "Tom and Nora in a yellow double sea kayak on turquoise water beside golden Algarve cliffs, paddles resting, both "
     "in plain orange life vests with no logos or writing, turned towards the camera and smiling, bright morning sun.", "Kayaking to Benagil")
shot("s52", "2025-08-14 13:15:27", "+01:00", "lagos_marina", "phone", [],
     "Lunch at a Portuguese seaside restaurant: grilled sardines, boiled potatoes, a tomato and onion salad and a glass "
     "of white wine on a table, the sea and boats softly blurred behind. Food photo from the diner's seat, no people.",
     "Grilled sardines in Lagos")
shot("s53", "2025-08-15 16:30:58", "+01:00", "luz", "phone", ["Nora", "Leo"],
     "At a holiday villa pool: Nora jumping into the water mid-air with her knees tucked, Leo in orange armbands sitting "
     "on the pool edge laughing; both faces visible, sunny afternoon, white walls and palm trees.", "Pool jumps at the villa")
shot("s54", "2025-08-16 11:00:41", "+01:00", "porto_de_mos", "camera:Algarve", ["Lucía"],
     "Lucía in a straw sun hat on a sandy beach, laughing at the camera, the wind in her hair, sea and cliffs softly "
     "blurred behind, bright late-morning light.", "Lucía at Porto de Mós")
shot("s55", "2025-08-17 20:10:15", "+01:00", "ponta_da_piedade", "phone", ["Lucía", "Tom", "Nora", "Leo"],
     "A family selfie on the cliff top at sunset: Tom holding the phone at arm's length, Lucía, Nora and Leo squeezed in "
     "beside him, all four smiling, golden sunset light on their faces, the sea and rock arches behind.",
     "Family selfie at sunset, Ponta da Piedade")
shot("s57", "2025-08-19 18:30:36", "+01:00", "meia_praia", "phone", ["Nora"],
     "Nora on a sandy beach holding up a big seashell next to her face, grinning at the camera, wet hair, warm "
     "early-evening light.", "Nora's seashell")
# First day of school, September 2025
shot("s58", "2025-09-01 08:05:12", "+02:00", "home", "phone", ["Nora", "Leo"],
     "First day of school: Nora and Leo standing at a dark-green front door with new backpacks, holding hands and "
     "smiling at the camera, bright morning sun; a plain doormat, no writing anywhere.", "First day of school", aspect="3:4")
shot("s59", "2025-09-01 08:31:40", "+02:00", "home", "phone", ["Lucía", "Leo"],
     "Lucía crouching to hug Leo goodbye at the gate of a red-brick Dutch primary school on the first day of school, "
     "Leo wearing his backpack, parents' bicycles parked behind (no buses); both faces visible and smiling towards the "
     "camera, morning light.", "A big hug at the school gate")
shot("s60", "2025-09-13 17:20:23", "+02:00", "grandparents", "phone", ["Jan"],
     "Grandpa Jan in his vegetable garden holding a basket of ripe tomatoes and courgettes, smiling at the camera, "
     "late-summer evening light, bean poles behind.", "Grandpa Jan's harvest")
shot("s61", "2025-10-25 19:10:05", "+02:00", "home", "phone", ["Lucía", "Tom"],
     "Lucía and Tom baking bread in a family kitchen in the evening, Tom with a smudge of flour on his cheek, both "
     "laughing at the camera, a loaf of dough on the floured counter, warm light.", "Baking bread on a Saturday night")
shot("s62", "2025-10-12 15:00:18", "+02:00", "home", "phone", ["Tom", "Leo"],
     f"Tom and Leo raking autumn leaves in a back garden, Leo throwing an armful of leaves in the air while {BISCUIT} "
     "jumps beside him; Tom and Leo laugh towards the camera, low autumn sun.", "Raking leaves", dog=True)
shot("s63", "2025-11-23 20:00:49", "+01:00", "home", "phone", ["Nora"],
     f"Nora in bed with a picture book open on her lap (its cover turned away from the camera) and {BISCUIT} lying "
     "beside her, a warm bedside lamp; Nora looks up at the camera and smiles.", "Bedtime reading with Biscuit", dog=True)
# Christmas 2025 at the grandparents'
shot("s64", "2025-12-20 16:30:26", "+01:00", "grandparents", "phone", ["Jan", "Nora"],
     "Nora standing on a stool placing a star on top of a Christmas tree while Grandpa Jan steadies her; both faces "
     "visible and smiling at the camera, fairy lights, cosy living room.", "The star on top", aspect="3:4")
shot("s65", "2025-12-21 15:20:43", "+01:00", "grandparents", "phone", ["Rosa", "Leo"],
     "Leo and Grandma Rosa baking Christmas cookies at a kitchen table, cookie cutters and dough, flour on Leo's nose; "
     "both look at the camera and laugh.", "Christmas cookies with Grandma Rosa")
shot("s66", "2025-12-24 19:30:17", "+01:00", "grandparents", "phone", ["Rosa"],
     "Grandma Rosa carrying a large dish of lasagna to a festive Christmas dinner table, candles and greenery, smiling at "
     "the camera, warm light.", "Grandma Rosa's lasagna, Christmas Eve 2025")
shot("s67", "2025-12-25 09:00:54", "+01:00", "grandparents", "phone", ["Lucía", "Tom", "Nora", "Leo"],
     "Christmas morning: Nora and Leo in pyjamas opening presents on the living-room floor by the Christmas tree, Lucía "
     "and Tom on the sofa behind them holding mugs of coffee; all four faces visible and smiling towards the camera.",
     "Christmas morning 2025")
shot("s68", "2025-12-25 13:00:32", "+01:00", "grandparents", "phone", ["Lucía", "Tom", "Nora", "Leo", "Rosa", "Jan"],
     "The Christmas family photo by the tree: Grandma Rosa and Grandpa Jan sitting on the sofa, Lucía and Tom standing "
     "behind them, Nora and Leo in front; all six smile at the camera, festive jumpers, warm living-room light. Medium "
     "shot, everyone's face large and sharp.", "The Christmas family photo",
     refs=["lucia", "tom", "nora9", "leo5", "rosa", "jan"])
shot("s69", "2025-12-26 14:10:21", "+01:00", "zandvoort", "phone", ["Tom", "Jan"],
     f"A windswept winter walk on the beach at Zandvoort: Tom and Grandpa Jan in warm coats with {BISCUIT} at their "
     "feet; both men smile at the camera, grey sea and dunes behind.", "Boxing Day beach walk", dog=True)
# -- 2026
shot("s70", "2026-01-17 20:15:08", "+01:00", "home", "phone", ["Tom", "Nora", "Leo"],
     "Board game night at the dining table: Tom, Nora and Leo around a colourful board game, laughing towards the camera, "
     "warm lamp light.", "Board game night")
# Ski week, February 2026 (Zillertal, Austria)
shot("s71", "2026-02-15 10:30:29", "+01:00", "penken", "camera:Zillertal", ["Nora"],
     "Nora on skis on a sunny groomed piste, helmet on with her goggles pushed up, smiling at the camera, snowy "
     "mountains and blue sky behind.", "Nora on the slopes")
shot("s72", "2026-02-15 11:15:46", "+01:00", "penken_school", "phone", ["Leo"],
     "Leo in ski school: a small boy in a helmet and a plain orange vest doing a snowplough down a gentle slope, "
     "laughing at the camera, sunny day, other little skiers far behind.", "Leo's first ski lesson")
shot("s73", "2026-02-16 12:40:03", "+01:00", "ahorn", "phone", ["Lucía", "Tom"],
     "A selfie of Lucía and Tom on a chairlift, sunglasses pushed up on their helmets, snowy peaks and blue sky behind, "
     "both smiling, faces sharp and lit by the sun.", "Chairlift selfie", aspect="3:4")
shot("s74", "2026-02-17 09:50:38", "+01:00", "hintertux", "camera:Zillertal", [],
     "Alpine panorama from a glacier ski area in Austria: snowy peaks under a deep blue sky, a groomed piste curving "
     "down, crisp morning light. Landscape photo, no people.", "Hintertux glacier")
shot("s75", "2026-02-17 13:00:15", "+01:00", "hintertux_hut", "phone", [],
     "Apple strudel with whipped cream and a mug of hot chocolate on a wooden table on a sunny mountain-hut terrace, ski "
     "gloves beside them, snowy peaks blurred behind. Food photo, no people.", "Strudel break")
shot("s76", "2026-02-18 13:20:57", "+01:00", "penken_hut", "camera:Zillertal", ["Lucía", "Tom", "Nora", "Leo"],
     "Lunch on a mountain-hut terrace: Lucía, Tom, Nora and Leo at a wooden table in ski jackets, goggles on their "
     "heads, bowls of soup and drinks, all four smiling at the camera, a medium shot so their faces are large, bright "
     "sun and snowy peaks behind.",
     "Lunch at the mountain hut")
shot("s77", "2026-02-19 16:30:12", "+01:00", "mayrhofen", "phone", ["Tom", "Leo"],
     "A snowball fight in an alpine village street: Leo throwing a snowball at Tom, who ducks and laughs; both faces "
     "visible, both in plain winter jackets with no logos or writing, snowy chalets behind, late-afternoon light.", "Snowball fight")
shot("s78", "2026-02-19 17:45:49", "+01:00", "mayrhofen", "camera:Zillertal", [],
     "An Austrian alpine village at dusk: snowy rooftops, a church spire and warm lit windows under a deep blue sky, "
     "mountains behind. Landscape photo, no people.", "Mayrhofen at dusk")
# Leo's 5th birthday, 14 March 2026
shot("s79", "2026-03-14 15:30:21", "+01:00", "home", "phone", ["Leo", "Nora", "Lucía"],
     "Leo blowing out five candles on a homemade green dinosaur cake at the kitchen table, Nora and Lucía behind him "
     "cheering; all three faces visible, balloons, warm afternoon light.", "Leo blows out five candles")
shot("s80", "2026-03-14 14:10:40", "+01:00", "home", "phone", ["Leo"],
     "Leo in a yellow paper party crown opening a present and finding a toy dinosaur, his face delighted and surprised, "
     "looking at the camera, wrapping paper and balloons around him.", "A dinosaur for the birthday boy")
shot("s81", "2026-03-14 14:45:06", "+01:00", "home", "phone", ["Leo"],
     "A children's birthday party in a back garden: Leo in front running towards the camera with a balloon and laughing, "
     "his face sharp, other small children playing with balloons softly blurred behind, sunny spring afternoon.",
     "Balloons in the garden")
shot("s82", "2026-03-14 16:10:33", "+01:00", "home", "phone", ["Jan", "Rosa", "Leo"],
     "Grandpa Jan and Grandma Rosa giving Leo a wrapped birthday present in a living room with balloons, Leo hugging "
     "Grandma Rosa; all three faces visible and smiling at the camera.", "A present from Rosa and Jan")
shot("s84", "2026-03-14 17:00:25", "+01:00", "home", "phone", ["Tom", "Leo"],
     "Tom with Leo on his shoulders, both wearing paper party hats, balloons around them in a living room, both laughing "
     "at the camera.", "Birthday boy on Dad's shoulders", aspect="3:4")
shot("s85", "2026-04-12 11:20:18", "+02:00", "home", "phone", ["Lucía", "Nora"],
     "Lucía and Nora planting vegetable seedlings in a wooden raised bed in a back garden, soil on their hands, both "
     "smiling at the camera, spring sunshine.", "Planting the vegetable garden")
shot("s86", "2026-05-30 13:00:47", "+02:00", "wilhelminapark", "phone", ["Lucía", "Tom", "Nora", "Leo"],
     f"A picnic in a green park: Lucía, Tom, Nora and Leo sitting on a checked blanket with a picnic basket, sandwiches "
     f"and strawberries, {BISCUIT} lying beside them; all four faces visible and smiling at the camera, sunny day.",
     "Picnic in the park", dog=True)
shot("s87", "2026-06-13 16:30:39", "+02:00", "home", "phone", ["Nora", "Leo"],
     "Nora and Leo blowing giant soap bubbles in a back garden with bubble wands, their faces visible and laughing "
     "through the shimmering bubbles, summer afternoon light.", "Giant bubbles")
shot("s88", "2026-07-19 16:00:14", "+02:00", "grandparents", "phone", ["Rosa", "Jan"],
     f"Grandma Rosa and Grandpa Jan sitting on a wooden garden bench holding hands, {BISCUIT} lying at their feet; both "
     "smile at the camera, summer roses behind.", "Rosa and Jan on their bench", dog=True)
shot("s89", "2026-08-08 15:30:52", "+02:00", "zandvoort", "phone", ["Tom", "Leo"],
     "Tom and Leo flying a red kite on a wide sandy beach in the Netherlands: a medium shot from the waist up, close "
     "enough that both faces are large, Leo holding the kite string with Tom kneeling beside him, both smiling at the "
     "camera, the kite high in the blue sky behind them, dunes in the distance.", "Flying the kite at Zandvoort")
shot("s91", "2026-09-05 19:00:31", "+02:00", "home", "phone", ["Lucía", "Nora"],
     "Pizza night: Nora spreading tomato sauce on pizza dough at the kitchen counter while Lucía grates cheese beside "
     "her; both smile at the camera, warm evening light.", "Homemade pizza night")
shot("s92", "2026-09-21 08:15:09", "+02:00", "wilhelminapark", "phone", ["Tom", "Leo"],
     "Leo riding a small bike along a leafy path on the way to school, Tom walking beside him with a hand on his "
     "shoulder; both faces visible and smiling at the camera, early-autumn morning sun.", "Cycling to school",
     aspect="3:4")
# Autumn walks, October 2026
shot("s93", "2026-10-04 15:20:44", "+02:00", "amelisweerd", "phone", ["Lucía", "Tom", "Nora", "Leo"],
     f"An autumn walk in a beech forest: Lucía, Tom, Nora and Leo - all four of them in the frame - throwing armfuls "
     f"of orange leaves into the air, {BISCUIT} leaping beside them; a medium-wide shot, all four faces visible and "
     "laughing towards the camera, golden afternoon light.",
     "Throwing leaves in Amelisweerd", dog=True)
shot("s94", "2026-10-04 15:50:27", "+02:00", "amelisweerd", "phone", ["Nora"],
     "Nora holding out two handfuls of shiny brown chestnuts towards the camera in an autumn forest, smiling, her face "
     "sharp, warm afternoon light.", "Nora's chestnuts", aspect="3:4")
shot("s95", "2026-10-11 11:10:16", "+02:00", "amelisweerd", "phone", ["Leo"],
     "Leo in yellow rain boots and a blue raincoat jumping into a muddy puddle on a forest path, a big splash, his face "
     "visible and laughing, autumn colours, overcast light.", "Puddle jumping")
shot("s96", "2026-10-11 11:40:03", "+02:00", "amelisweerd", "phone", [],
     "An autumn beech forest with sunbeams falling through orange and golden leaves onto a path covered in leaves. "
     "Landscape photo, no people.", "Autumn light in Amelisweerd", aspect="3:4")

# The library first reused the 20 sample photos of docs/promo/source/library (720 px, with
# strangers' faces that became stray people). These are fresh photos of the same subjects, as the
# family's own: the same dates and places, the people from the family or seen from behind.
BEHIND = ("seen from behind, so no faces are visible")
shot("r01", "2024-10-19 15:30:12", "+02:00", "amelisweerd", "phone", ["Nora", "Leo"],
     "Two children, a 7-year-old girl with long light-brown hair in a yellow raincoat and a 3-year-old boy with "
     f"straight dark-brown hair in a red raincoat, running hand in hand down a forest path covered in orange autumn "
     f"leaves, {BEHIND}, warm low afternoon sun through the beech trees.",
     "Nora and Leo racing down the leafy path", refs=[], faces=False)
shot("r02", "2024-11-03 15:02:47", "+01:00", "home", "phone", [],
     f"{BISCUIT[0].upper() + BISCUIT[1:]}, leaping through a pile of autumn leaves in a back garden, leaves flying in the "
     "air, late afternoon light. Pet photo, no people.", "Biscuit leaping through the leaf pile", dog=True)
shot("r03", "2024-10-31 19:20:33", "+01:00", "home", "phone", ["Nora", "Leo"],
     "Halloween evening: Nora and Leo carving a big orange pumpkin at the kitchen table, scooping out the seeds with "
     "spoons, a finished jack-o'-lantern glowing with a candle beside them; both look up at the camera and laugh, warm "
     "light.", "Pumpkin carving for Halloween")
shot("r04", "2024-07-21 06:12:05", "+02:00", "braies", "camera:Dolomites", [],
     "Misty sunrise over Lago di Braies in the Dolomites: a calm lake with a wooden boathouse and its dock, pine "
     "forest and steep mountains, soft fog on the water, pastel sky. Landscape photo, no people.",
     "Misty sunrise at Lago di Braies", aspect="2:3")
shot("r05", "2024-01-27 16:05:38", "+01:00", "home", "phone", [],
     "Two mugs of hot chocolate with marshmallows on a wooden table by a window, raindrops on the glass, a blanket on "
     "the chair, cosy afternoon light. No people.", "Hot chocolate on a rainy afternoon")
shot("r06", "2024-10-12 11:05:19", "+02:00", None, "phone", ["Nora"],
     "Nora in yellow rain boots and a red coat jumping into a puddle on a quiet street of brick houses, the splash "
     "frozen mid-air, her face visible and laughing, overcast autumn day.", "Puddle jumping on the way home")
shot("r07", "2025-09-01 08:25:44", "+02:00", None, "phone", ["Nora", "Leo"],
     "First day of school: an 8-year-old girl with long light-brown hair and a 4-year-old boy with straight "
     "dark-brown hair, both with new backpacks, walking hand in hand towards the open gate of a red-brick primary "
     f"school, {BEHIND}, bright morning sun; a few other children far away, also from behind.",
     "Walking through the school gate together", refs=[], faces=False)
shot("r08", "2025-05-03 18:40:16", "+02:00", None, "phone", ["Jan", "Nora"],
     "Grandpa Jan and Nora planting tomato seedlings together in an allotment vegetable garden, kneeling by the soil, "
     "both smiling at the camera, golden evening light.", "Planting tomatoes at the allotment with Grandpa Jan")
shot("r09", "2024-11-10 16:45:02", "+01:00", "grandparents", "phone", [],
     "A homemade apple pie with a lattice crust cooling on a rack on a kitchen counter, a few apples beside it, soft "
     "window light. Food photo, no people.", "Grandma Rosa's apple pie, fresh from the oven")
shot("r10", "2025-07-12 18:20:41", "+02:00", "bunnik", "phone", ["Lucía", "Tom", "Nora", "Leo"],
     "A family of four riding bicycles on a quiet Dutch country road between sunflower fields: a dark-haired mother, "
     "a sandy-haired father, a girl with a long light-brown braid and a small boy with dark hair on a child's bike, "
     f"a wide shot from a little behind, {BEHIND}, warm summer evening light.",
     "Cycling past the sunflower fields", refs=[], faces=False)
shot("r11", "2025-08-11 20:35:27", "+01:00", "meia_praia", "camera:Algarve", ["Lucía", "Tom", "Leo"],
     "Beach at sunset in the Algarve: dark silhouettes of a mother and father swinging their small son between them "
     "at the water's edge, the orange sky reflected on the wet sand, no faces visible.",
     "Sunset swing at Meia Praia", refs=[], faces=False)
shot("r12", "2024-08-10 14:20:55", "+02:00", "zandvoort", "phone", ["Nora", "Leo"],
     "Nora and Leo building a sandcastle with buckets and spades on a sunny Dutch beach, close-up, both faces visible "
     "and smiling at the camera, the sea and dunes in the background.", "Building a sandcastle at Zandvoort")
shot("r13", "2025-08-13 15:20:08", "+01:00", "carvoeiro", "camera:Algarve", [],
     "A small white sailboat on turquoise water below golden limestone cliffs in the Algarve, seen from the cliff "
     "top, bright summer day. Landscape photo, no people.", "A sailboat off the cliffs near Carvoeiro")
shot("r14", "2024-07-23 11:50:39", "+02:00", "tre_cime", "camera:Dolomites", ["Lucía", "Tom", "Nora", "Leo"],
     "A family of four hiking on a mountain trail high above an alpine lake in the Dolomites, dramatic peaks and "
     "clouds, the family as small figures in the landscape seen from behind.",
     "Hiking above the lake", refs=[], faces=False, aspect="2:3")
shot("r15", "2024-07-25 23:40:17", "+02:00", "cortina_camp", "camera:Dolomites", [],
     "A small tent glowing warmly at night on a mountain meadow in the Dolomites under the Milky Way, long exposure, "
     "starry sky, jagged peaks in silhouette. Landscape photo, no people.", "Our tent under the Milky Way")
shot("r16", "2024-06-15 16:40:26", "+02:00", None, "phone", ["Nora", "Leo"],
     "Nora and Leo laughing with dripping ice-cream cones on a sunny old-town street in summer, both faces visible "
     "and turned to the camera, café terraces softly blurred behind.", "Ice creams in the old town")
shot("r17", "2025-08-09 20:25:31", "+01:00", "cabo_da_roca", "camera:Algarve", [],
     "The lighthouse at Cabo da Roca in Portugal at golden hour: a white tower with a red top on the green cliffs above "
     "the Atlantic, waves breaking on the rocks below. Landscape photo, no people.",
     "Cabo da Roca lighthouse at golden hour, on the way south")
shot("r18", "2025-08-12 08:10:49", "+01:00", "meia_praia", "phone", [],
     "A black-and-white border collie running along the shoreline of a long sandy beach with a ball in its mouth, "
     "water splashing, early morning light. Pet photo, no people.", "A friendly collie on the beach in the morning")
shot("r19", "2025-12-05 17:30:12", "+01:00", "home", "phone", ["Nora", "Lucía", "Tom", "Leo"],
     "Nora's 9th birthday: Nora blowing out nine candles on a homemade chocolate cake at the dining table, Lucía, Tom "
     "and Leo around her cheering and clapping, warm evening light; all four faces visible.", "Nora turns nine")
shot("r20", "2025-11-15 11:30:04", "+01:00", None, "phone", [],
     "A newborn baby's tiny hand holding an adult's finger, very soft window light, shallow depth of field; no faces "
     "in the frame.", "Holding baby Emma's hand, the neighbours' newborn")
REUSED = []


def compose(s):
    """The full prompt text for a planned shot, and the reference keys passed with it (in order)."""
    day = dt.date.fromisoformat(s["when"][:10])
    refs = s["refs"] if s["refs"] is not None else [ref_key(n, day) for n in s["people"]]
    if s["dog"]:
        refs = refs + ["biscuit"]
    style = STYLE_CAMERA if s["src"].startswith("camera") else STYLE_PHONE
    parts = []
    if refs:
        lines = []
        for i, r in enumerate(refs, 1):
            if r == "biscuit":
                lines.append(f"reference photo {i} is the family dog, Biscuit, a golden retriever")
            else:
                name = PORTRAIT_PEOPLE[r]
                at = REF_AGE.get(r)
                lines.append(f"reference photo {i} is {DISPLAY[name]}" + (f" ({at} there)" if at else ""))
        people = "; ".join(describe(n, day) for n in s["people"])
        parts.append("The attached reference photos show who is in this new photo: " + "; ".join(lines) + ". "
                     "Make a NEW, different photo of these same people" + (" and dog" if s["dog"] else "") + ": keep each "
                     "person clearly recognisable - the same face shape, eyes, nose, mouth, skin tone and hair colour as "
                     "in their reference photo - but at the age, in the clothes, pose, place and light described below. "
                     "Do not copy the reference photos' backgrounds, clothes or framing." +
                     (f" In this photo: {people}." if people else ""))
    parts.append(s["scene"])
    if s["people"] and s["faces"]:
        parts.append(FACES)
    parts.append(style)
    return "\n\n".join(parts), refs

def plan():
    out = []
    for key, p in PORTRAITS.items():
        text = p["scene"] + "\n\n" + ("Looks like a real photo a family member took with a good phone: natural skin "
               "texture, no make-up look, not retouched, not a studio shot. No text, no watermark, no border, no frame, "
               "not a collage.")
        out.append(dict(id="p_" + key, kind="portrait", ref_key=key, model="pro", aspect="3:4", size="2K",
                        refs=p["refs"], prompt=text, when=p["when"], off=p["off"], place=p["place"], src="phone",
                        people=[PORTRAIT_PEOPLE[key]], desc=p["desc"]))
    for s in S:
        text, refs = compose(s)
        out.append(dict(id=s["id"], kind="scene", model=s["model"], aspect=s["aspect"],
                        size="2K" if s["model"] == "pro" else "-", refs=refs, prompt=text, when=s["when"], off=s["off"],
                        place=s["place"], src=s["src"], people=s["people"], desc=s["desc"],
                        ref_key="biscuit" if s["id"] == "biscuit" else None))
    for nn, when, off, place, src, people, unnamed, desc in REUSED:
        out.append(dict(id="r" + nn, kind="reused", source=f"docs/promo/source/library/{nn}.jpg", when=when, off=off,
                        place=place, src=src, people=people, unnamed_faces=unnamed, desc=desc))
    for o in out:
        o["gps"] = list(P[o["place"]]) if o.get("place") else None
    return out

if __name__ == "__main__":
    root = sys.argv[1]
    os.makedirs(os.path.join(root, "prompts"), exist_ok=True)
    items = plan()
    for o in items:
        if o["kind"] == "reused":
            continue
        with open(os.path.join(root, "prompts", o["id"] + ".txt"), "w") as f:
            f.write(o["prompt"])
    json.dump(dict(people=DISPLAY, places=P, items=items), open(os.path.join(root, "prompts", "plan.json"), "w"),
              ensure_ascii=False, indent=1)
    n = {k: sum(1 for o in items if o["kind"] == k) for k in ("portrait", "scene", "reused")}
    pro = sum(1 for o in items if o.get("model") == "pro")
    print(n, "pro:", pro, "flash:", sum(1 for o in items if o.get("model") == "flash"))
