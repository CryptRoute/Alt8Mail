/*
 * Plural categories the webmail can actually select for small UI counts.
 *
 * CLDR exposes categories such as French "many" for 1,000,000, but mailbox
 * counts in normal use are small. Sampling 0-100 keeps Russian "few/many" and
 * Arabic "zero/two/few/many" mandatory without blocking a locale on a category
 * the UI will not realistically hit (French "many" starts at one million).
 */
export function requiredPluralForms(locale, max = 100) {
  try {
    Intl.getCanonicalLocales(locale);
    const rules = new Intl.PluralRules(locale);
    const forms = new Set(["other"]);
    for (let count = 0; count <= max; count += 1) forms.add(rules.select(count));
    return [...forms];
  } catch {
    return ["other"];
  }
}
