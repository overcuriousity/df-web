-- web-units: dump all citizen dwarves with their labor assignments as JSON.
-- Invoked via: dfhack-run web-units
-- Output: one JSON array on stdout, consumed by the session-manager labor panel.

local json = require('json')
local units = df.global.world.units.active
local unit_labor = df.unit_labor

local result = {}
for i = 0, #units - 1 do
    local u = units[i]
    if dfhack.units.isCitizen(u) and dfhack.units.isAlive(u) then
        local labors = {}
        for labor_id = 0, 119 do
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
