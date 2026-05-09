-- web-units: dump all citizen dwarves with their labor assignments as JSON.
-- Invoked via: dfhack-run web-units
-- Output: one JSON array on stdout, consumed by the session-manager labor panel.

local json = require('json')
local units = df.global.world.units.active

-- The labors array is sized to df.unit_labor; older DFHack scripts hard-coded
-- 119 (the DF 0.47 ceiling). DF 53.x trimmed the enum to ~94 entries, and
-- reading past the end raises "index out of bounds" — which the proxy then
-- reports as "DFHack unavailable". _last_item tracks whatever the running DF
-- build actually has.
local last_labor = df.unit_labor._last_item

local result = {}
for i = 0, #units - 1 do
    local u = units[i]
    if dfhack.units.isCitizen(u) and dfhack.units.isAlive(u) then
        local labors = {}
        for labor_id = 0, last_labor do
            if u.status.labors[labor_id] then
                labors[#labors + 1] = labor_id
            end
        end
        result[#result + 1] = {
            id         = u.id,
            name       = dfhack.TranslateName(dfhack.units.getVisibleName(u)),
            profession = dfhack.units.getProfessionName(u),
            labors     = labors,
        }
    end
end

print(json.encode(result))
