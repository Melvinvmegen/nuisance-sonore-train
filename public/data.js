// Couche données, 100 % navigateur.
// - Trains : API SNCF (Navitia) via le relais nginx /sncf/{arrivals,departures} (nginx ajoute la clé et met en cache).
//   Si le relais répond en erreur (pas de clé, API en panne…) → open data SNCF (/opendata.json),
//   et si lui aussi manque → données de démo simulées.
// - Mes trains bruyants : GET /declarer?… (nginx l'écrit dans son journal) et lecture de /declarations.jsonl.

const TrainData = (() => {
  // Lignes souterraines qui ne passent pas le long de la rue (format "<mode> <code>", minuscules)
  const EXCLUDE_LINES = ['rer a'];

  // ---------- Fuseau Europe/Paris ----------
  const parisFmt = new Intl.DateTimeFormat('en-US', {
    timeZone: 'Europe/Paris', hourCycle: 'h23',
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
  const parts = (d) => Object.fromEntries(parisFmt.formatToParts(d).map((x) => [x.type, x.value]));
  const parisOffsetMs = (ts) => {
    const p = parts(new Date(ts));
    return Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second) - ts;
  };
  // "20261102T143000" (heure de Paris) -> Date
  const parseNavitia = (s) => {
    if (!s) return null;
    const n = (a, b) => Number(s.slice(a, b));
    const guess = Date.UTC(n(0, 4), n(4, 6) - 1, n(6, 8), n(9, 11), n(11, 13), n(13, 15));
    return new Date(guess - parisOffsetMs(guess));
  };
  const parisDay = (d) => { const p = parts(d); return `${p.year}${p.month}${p.day}`; };

  // Journée en cours (heure de Paris) ; les données couvrent 00h → 03h le lendemain (27 h)
  function dayRange(now = new Date()) {
    const today = parisDay(now);
    const start = parseNavitia(today + 'T000000');
    const t = new Date(Date.UTC(+today.slice(0, 4), +today.slice(4, 6) - 1, +today.slice(6, 8) + 1));
    const end = parseNavitia(t.toISOString().slice(0, 10).replaceAll('-', '') + 'T000000');
    return { today, dayStart: start, dayEnd: end, to: new Date(end.getTime() + 3 * 3600e3) };
  }

  // ---------- Live : relais nginx ----------
  async function fetchKind(kind, today) {
    const PER_PAGE = 1000, out = [];
    for (let page = 0; page < 3; page++) {
      // Paramètres identiques pour tous les visiteurs d'une même journée → le cache nginx est partagé
      const q = new URLSearchParams({
        from_datetime: today + 'T000000', duration: String(27 * 3600), count: String(PER_PAGE),
        start_page: String(page), data_freshness: 'realtime', depth: '1',
      });
      const r = await fetch(`/sncf/${kind}?${q}`);
      if (!r.ok) throw new Error(r.status === 401 ? 'clé API SNCF absente ou invalide (HTTP 401)' : `API SNCF ${kind} : HTTP ${r.status}`);
      const json = await r.json();
      const items = json[kind] || [];
      out.push(...items);
      const total = json.pagination?.total_result;
      if (items.length < PER_PAGE || (total != null && out.length >= total)) break;
    }
    const type = kind === 'arrivals' ? 'arrival' : 'departure';
    return out.map((it) => {
      const di = it.display_informations || {}, sdt = it.stop_date_time || {};
      const actual = parseNavitia(type === 'arrival' ? sdt.arrival_date_time : sdt.departure_date_time);
      const base = parseNavitia(type === 'arrival' ? sdt.base_arrival_date_time : sdt.base_departure_date_time) || actual;
      const mode = di.commercial_mode || di.physical_mode || 'Train';
      const term = it.route?.direction?.stop_area?.coord; // terminus du trajet, sert à repérer le sens (voir throughOnce)
      return {
        towardStreet: term ? streetSide(+term.lat, +term.lon) : null,
        id: `${type}-${di.trip_short_name || di.headsign}-${base?.toISOString()}`,
        type, mode, line: di.code || '',
        time: actual?.toISOString(), baseTime: base?.toISOString(),
        delayMin: actual && base ? Math.round((actual - base) / 60000) : 0,
        number: di.trip_short_name || di.headsign || '',
        direction: type === 'arrival' && /gare de lyon/i.test(di.direction || '') ? 'Terminus Paris Gare de Lyon' : (di.direction || ''),
        color: di.color ? `#${di.color}` : null,
      };
    }).filter((x) => x.time && !EXCLUDE_LINES.includes(`${x.mode} ${x.line}`.toLowerCase().trim()));
  }

  // ---------- Trains qui traversent la gare (RER D) : un seul passage devant la rue ----------
  // Les voies qui longent la rue partent de la gare vers le sud-est (mêmes valeurs que opendata/main.go).
  const GDL = [48.8443, 2.3739], STREET_DIR = [-0.40, 0.92]; // (nord, est) unitaire : gare → voies côté rue
  const streetSide = (lat, lon) => (lat - GDL[0]) * STREET_DIR[0] + (lon - GDL[1]) * 0.658 * STREET_DIR[1] > 0;
  // Un train présent en arrivée ET en départ traverse la gare : s'il repart vers la rue, il la longe après son départ
  // (on retire l'arrivée), sinon avant son arrivée (on retire le départ). Sens inconnu : on garde les deux.
  function throughOnce(arrivals, departures) {
    const MAX_STOP = 15 * 60e3;
    const byNum = new Map(departures.map((d) => [d.number, d]));
    const dropped = new Set();
    for (const a of arrivals) {
      const d = byNum.get(a.number);
      if (!a.number || !d || Math.abs(new Date(d.time) - new Date(a.time)) > MAX_STOP) continue;
      const toward = d.towardStreet ?? a.towardStreet;
      if (toward === true) dropped.add(a);
      else if (toward === false) dropped.add(d);
    }
    return [...arrivals, ...departures].filter((x) => !dropped.has(x));
  }

  // ---------- Démo : grille simulée, identique chaque jour ----------
  const DEMO_LINES = [
    { mode: 'TGV INOUI', line: '', color: '#9b2743', dirs: ['Marseille Saint-Charles', 'Lyon Part-Dieu', 'Montpellier Saint-Roch', 'Nice Ville', 'Grenoble'] },
    { mode: 'OUIGO', line: '', color: '#e5007d', dirs: ['Marseille Saint-Charles', 'Lyon Saint-Exupéry', 'Montpellier Sud de France'] },
    { mode: 'TER', line: '', color: '#2b6cb0', dirs: ['Dijon Ville', 'Montereau', 'Laroche-Migennes'] },
    { mode: 'Transilien', line: 'R', color: '#e4b4d1', dirs: ['Montargis', 'Montereau', 'Melun'] },
    { mode: 'RER', line: 'D', color: '#5e9620', dirs: ['Melun', 'Corbeil-Essonnes', 'Malesherbes', 'Creil'] },
    { mode: 'TGV Lyria', line: '', color: '#b8002d', dirs: ['Genève', 'Lausanne', 'Zurich HB'] },
    { mode: 'Frecciarossa', line: '', color: '#c00000', dirs: ['Milano Centrale', 'Lyon Part-Dieu'] },
  ];
  const WEIGHTS = [0.3, 0.1, 0.08, 0.12, 0.3, 0.05, 0.05];
  const hash = (n) => { // PRNG déterministe (mulberry32)
    let t = (n + 0x6d2b79f5) | 0;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  const pick = (r) => { let acc = 0; for (let i = 0; i < WEIGHTS.length; i++) { acc += WEIGHTS[i]; if (r < acc) return i; } return WEIGHTS.length - 1; };
  const density = (d) => { // quasi rien la nuit, pointes matin et soir
    const h = +parts(d).hour;
    if (h >= 1 && h < 5) return 0.02;
    if (h < 6 || h >= 23) return 0.12;
    if ((h >= 7 && h < 10) || (h >= 17 && h < 20)) return 1.15;
    return 0.85;
  };
  function demoData(from, to) {
    const items = [];
    for (let m = Math.floor(from / 60000); m <= Math.floor(to / 60000); m++) {
      const k = density(new Date(m * 60000));
      for (const [type, salt, p] of [['arrival', 1, 0.33 * k], ['departure', 2, 0.3 * k]]) {
        const seed = (m % 1440) * 7 + salt;
        if (hash(seed) > p) continue;
        const L = DEMO_LINES[pick(hash(seed + 11))];
        const r = hash(seed + 37);
        const delayMin = r > 0.85 ? Math.ceil(hash(seed + 41) * 25) : r > 0.7 ? Math.ceil(hash(seed + 43) * 5) : 0;
        const base = new Date(m * 60000);
        const number = String(Math.floor(1000 + hash(seed + 53) * 9000));
        items.push({
          id: `${type}-${number}-${base.toISOString()}`, type, mode: L.mode, line: L.line, number, color: L.color,
          time: new Date(base.getTime() + delayMin * 60000).toISOString(), baseTime: base.toISOString(), delayMin,
          direction: type === 'arrival'
            ? `de ${L.dirs[Math.floor(hash(seed + 29) * L.dirs.length)]}`
            : L.dirs[Math.floor(hash(seed + 23) * L.dirs.length)],
        });
      }
    }
    return items;
  }

  // ---------- Secours : open data SNCF préparé côté serveur (opendata/main.go → /opendata.json) ----------
  // Horaires prévus + retards temps réel quand ils existent (surtout TGV/Intercités) ; item.realtime l'indique.
  async function fetchOpenData(today) {
    const r = await fetch('/opendata.json', { cache: 'no-store' });
    if (!r.ok) throw new Error(`données ouvertes : HTTP ${r.status}`);
    const j = await r.json();
    if (j.day !== today) throw new Error('données ouvertes pas à jour (autre journée)');
    if (Date.now() - new Date(j.generatedAt) > 15 * 60e3) throw new Error('données ouvertes figées depuis plus de 15 min');
    return j;
  }

  // API SNCF → open data SNCF → démo
  async function load({ forceDemo = false } = {}) {
    const { today, dayStart, dayEnd, to } = dayRange();
    let source = 'live', items, error = null, realtimeAt = null;
    if (forceDemo) { source = 'demo'; items = demoData(dayStart, to); }
    else {
      try {
        const [a, d] = await Promise.all([fetchKind('arrivals', today), fetchKind('departures', today)]);
        items = throughOnce(a, d);
      } catch (e) {
        error = e.message;
        try {
          const j = await fetchOpenData(today);
          source = 'opendata'; items = j.items; realtimeAt = j.realtimeAt;
        } catch (e2) {
          source = 'demo'; error += ' · ' + e2.message; items = demoData(dayStart, to);
        }
      }
    }
    items.sort((x, y) => new Date(x.time) - new Date(y.time));
    return { source, error, items, realtimeAt, dayStart: dayStart.toISOString(), dayEnd: dayEnd.toISOString(), generatedAt: new Date().toISOString() };
  }

  return { load };
})();

// ---------- Mes trains bruyants, stockés dans le journal nginx de mon instance ----------
const Declarations = (() => {
  // n = 1 : « ce train est bruyant », n = 0 : retrait. h et dir servent à l'affichage de la liste.
  async function send(trainKey, n, info = {}) {
    const q = new URLSearchParams({ train: trainKey, n: String(n), h: info.at || '', dir: info.direction || '' });
    try {
      const r = await fetch(`/declarer?${q.toString().replaceAll('+', '%20')}`, { cache: 'no-store', keepalive: true });
      return r.status === 204;
    } catch { return false; }
  }

  // Retourne { clé: { type, mode, line, number, direction, at } } (dernière déclaration n=1 de chaque train),
  // ou null si le serveur est injoignable.
  async function load() {
    try {
      const r = await fetch('/declarations.jsonl', { cache: 'no-store' });
      if (!r.ok) return null;
      const latest = new Map();
      for (const line of (await r.text()).split('\n')) {
        if (!line.trim()) continue;
        let x; try { x = JSON.parse(line); } catch { continue; }
        if (x.s !== '204') continue; // requêtes refusées
        const dec = (v) => { try { return decodeURIComponent(v || ''); } catch { return ''; } };
        const key = dec(x.train); if (!key) continue;
        const t = Number(x.t), prev = latest.get(key);
        if (!prev || t >= prev.t) latest.set(key, { t, on: x.n === '1', at: dec(x.h), direction: dec(x.dir) });
      }
      const out = {};
      for (const [key, v] of latest) {
        if (!v.on) continue;
        const [type, mode, line, number] = key.split('|');
        out[key] = { type, mode, line, number, direction: v.direction, at: v.at };
      }
      return out;
    } catch { return null; }
  }

  return { send, load };
})();
