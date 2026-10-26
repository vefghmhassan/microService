// Persian names for Iran's provinces as GeoIP databases spell them, and for
// the big Iranian ISPs, so the report reads naturally.

const provinces: Record<string, string> = {
  tehran: 'تهران', alborz: 'البرز', isfahan: 'اصفهان', esfahan: 'اصفهان', fars: 'فارس', khuzestan: 'خوزستان',
  'razavi khorasan': 'خراسان رضوی', 'khorasan-e razavi': 'خراسان رضوی', 'north khorasan': 'خراسان شمالی', 'south khorasan': 'خراسان جنوبی',
  'east azerbaijan': 'آذربایجان شرقی', 'west azerbaijan': 'آذربایجان غربی', 'azarbayjan-e sharqi': 'آذربایجان شرقی', 'azarbayjan-e gharbi': 'آذربایجان غربی',
  mazandaran: 'مازندران', gilan: 'گیلان', golestan: 'گلستان', kerman: 'کرمان', kermanshah: 'کرمانشاه', hormozgan: 'هرمزگان',
  'sistan and baluchestan': 'سیستان و بلوچستان', 'sistan va baluchestan': 'سیستان و بلوچستان', lorestan: 'لرستان', hamadan: 'همدان',
  markazi: 'مرکزی', qazvin: 'قزوین', ardabil: 'اردبیل', yazd: 'یزد', zanjan: 'زنجان', qom: 'قم', kurdistan: 'کردستان', kordestan: 'کردستان',
  bushehr: 'بوشهر', 'chaharmahal and bakhtiari': 'چهارمحال و بختیاری', 'chahar mahal and bakhtiari': 'چهارمحال و بختیاری', semnan: 'سمنان',
  'kohgiluyeh and boyer-ahmad': 'کهگیلویه و بویراحمد', 'kohgiluyeh and boyer-ahmad province': 'کهگیلویه و بویراحمد', ilam: 'ایلام',
}

const norm = (s: string) => s.toLowerCase().replace(/^ostan-e\s+/, '').replace(/\s+province$/, '').trim()

export function provinceName(region: string, country: string): string {
  if (!region) return country === 'IR' ? 'نامشخص (ایران)' : 'نامشخص'
  return (country === 'IR' && provinces[norm(region)]) || region
}

const isps: [RegExp, string][] = [
  [/mobile communication company of iran|\bmci\b|hamrah/i, 'همراه اول'],
  [/mtn ?irancell|iran ?cell/i, 'ایرانسل'],
  [/rightel|tamin telecom/i, 'رایتل'],
  [/iran telecommunication company|\btci\b|telecommunication company of iran/i, 'مخابرات'],
  [/shatel|aria shatel/i, 'شاتل'],
  [/asiatech/i, 'آسیاتک'],
  [/pars online|parsonline/i, 'پارس‌آنلاین'],
  [/respina/i, 'رسپینا'],
  [/mobinnet/i, 'مبین‌نت'],
  [/pishgaman/i, 'پیشگامان'],
  [/afranet/i, 'افرانت'],
  [/hiweb/i, 'های‌وب'],
  [/zitel/i, 'زی‌تل'],
]

export function ispName(isp: string): string {
  for (const [re, fa] of isps) if (re.test(isp)) return `${fa} · ${isp}`
  return isp || 'نامشخص'
}

export const flag = (cc: string) =>
  cc && cc.length === 2 ? String.fromCodePoint(...[...cc.toUpperCase()].map((c) => 0x1f1a5 + c.charCodeAt(0))) : '🌐'
